package goboot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
)

type bootSystem interface {
	mountSystem
	MakeMountsPrivate() error
	LoadModules(string, []string) error
	SwitchRoot(MountConfig) error
	SetHostname(string) error
	SetupNetwork([]InterfaceConfig) error
	WriteSystemFiles(*Config) error
	StartService(ServiceConfig, []string) (serviceProcess, error)
}

type serviceProcess interface {
	Wait() error
	Signal(os.Signal) error
	Kill() error
	PID() int
}

type commandProcess struct{ command *exec.Cmd }

func (p commandProcess) Wait() error { return p.command.Wait() }

func (p commandProcess) Signal(signal os.Signal) error { return p.command.Process.Signal(signal) }

func (p commandProcess) Kill() error { return p.command.Process.Kill() }

func (p commandProcess) PID() int { return p.command.Process.Pid }

type linuxBootSystem struct {
	linuxMountSystem
}

func (linuxBootSystem) MakeMountsPrivate() error {
	return MakeMountsPrivate()
}

func (linuxBootSystem) LoadModules(tree string, modules []string) error {
	return LoadModules(tree, modules)
}

func (linuxBootSystem) SwitchRoot(root MountConfig) error { return SwitchRoot(root) }

func (linuxBootSystem) SetupNetwork(interfaces []InterfaceConfig) error {
	return SetupNetwork(interfaces)
}

func (linuxBootSystem) WriteSystemFiles(cfg *Config) error {
	return WriteSystemFiles(cfg)
}

// MakeMountsPrivate stops mount propagation to other namespaces.
func MakeMountsPrivate() error {
	return syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, "")
}

func (linuxBootSystem) SetHostname(name string) error {
	return SetHostname(name)
}

// SetHostname sets the Linux UTS hostname.
func SetHostname(name string) error {
	if name == "" || len(name) > 64 || strings.ContainsAny(name, "\x00\n") {
		return errors.New("hostname must contain 1 to 64 bytes and no NUL or newline")
	}
	return syscall.Sethostname([]byte(name))
}

func (linuxBootSystem) StartService(service ServiceConfig, environment []string) (serviceProcess, error) {
	command := exec.Command(service.Command[0], service.Command[1:]...)
	command.Dir = service.Directory
	if command.Dir == "" {
		command.Dir = "/"
	}
	command.Env = environment
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	return commandProcess{command: command}, nil
}

// Run performs boot setup, starts configured services, and returns control to
// the caller. The caller owns its PID 1 lifecycle and may call Runtime.Wait.
// Outside PID 1, isolated mount and UTS namespaces are required.
func Run(ctx context.Context, cfg *Config) (*Runtime, error) {
	if err := validateRunnable(cfg); err != nil {
		return nil, err
	}
	if err := ensureDevelopmentIsolation(cfg); err != nil {
		return nil, err
	}
	logger, closer, err := OpenLogger(cfg.Logging.Output)
	if err != nil {
		return nil, err
	}
	runtime, err := run(ctx, cfg, logger, closer, linuxBootSystem{})
	if err != nil {
		logger.Error("boot failed", "error", err)
		closer.Close()
		return nil, err
	}
	return runtime, nil
}

func ensureDevelopmentIsolation(cfg *Config) error {
	if os.Getpid() == 1 {
		return nil
	}
	for _, namespace := range []string{"mnt", "uts", "net"} {
		if namespace == "uts" && cfg.Hostname == "" {
			continue
		}
		self, err := os.Readlink("/proc/self/ns/" + namespace)
		if err != nil {
			return fmt.Errorf("inspect %s namespace: %w", namespace, err)
		}
		init, err := os.Readlink("/proc/1/ns/" + namespace)
		if err != nil {
			return fmt.Errorf("inspect PID 1 %s namespace: %w", namespace, err)
		}
		if self == init {
			return fmt.Errorf("refusing to change the active %s namespace; run in an isolated namespace", namespace)
		}
	}
	return nil
}

func validateRunnable(cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return nil
}

func run(ctx context.Context, cfg *Config, logger *slog.Logger, closer io.Closer, system bootSystem) (*Runtime, error) {
	if err := system.MakeMountsPrivate(); err != nil {
		return nil, fmt.Errorf("make mount propagation private: %w", err)
	}
	if len(cfg.Modules) != 0 {
		logger.Info("loading kernel modules", "count", len(cfg.Modules))
		if err := system.LoadModules(cfg.ModuleTree, cfg.Modules); err != nil {
			return nil, fmt.Errorf("load kernel modules: %w", err)
		}
	}
	if cfg.Root != nil {
		logger.Info("switching root filesystem", "target", cfg.Root.Target)
		if err := system.SwitchRoot(*cfg.Root); err != nil {
			return nil, fmt.Errorf("switch root: %w", err)
		}
	}
	logger.Info("setting up filesystems")
	if err := setupMounts(cfg, system); err != nil {
		return nil, err
	}
	if cfg.Hostname != "" {
		logger.Info("setting hostname", "hostname", cfg.Hostname)
		if err := system.SetHostname(cfg.Hostname); err != nil {
			return nil, fmt.Errorf("set hostname %q: %w", cfg.Hostname, err)
		}
	}
	logger.Info("setting up network")
	if err := system.SetupNetwork(cfg.Interfaces); err != nil {
		return nil, fmt.Errorf("set up network: %w", err)
	}
	if cfg.ResolvConf.Present || cfg.Hosts.Present {
		logger.Info("writing system files")
		if err := system.WriteSystemFiles(cfg); err != nil {
			return nil, err
		}
	}
	return startServices(ctx, cfg, logger, closer, system)
}

func serviceEnvironment(global, local map[string]string) []string {
	values := map[string]string{
		"HOME": "/",
		"PATH": "/usr/bin:/bin",
	}
	for key, value := range global {
		values[key] = value
	}
	for key, value := range local {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}
