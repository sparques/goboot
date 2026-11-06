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
	SetHostname(string) error
	StartService(context.Context, ServiceConfig, []string) (serviceProcess, error)
}

type serviceProcess interface {
	Wait() error
}

type linuxBootSystem struct {
	linuxMountSystem
}

func (linuxBootSystem) MakeMountsPrivate() error {
	return MakeMountsPrivate()
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

func (linuxBootSystem) StartService(ctx context.Context, service ServiceConfig, environment []string) (serviceProcess, error) {
	command := exec.CommandContext(ctx, service.Command[0], service.Command[1:]...)
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
	return command, nil
}

// Runtime tracks services started by Run. It does not own the caller's process.
type Runtime struct {
	done chan struct{}
	err  error
}

// Wait reports the service's exit. It can be called more than once.
func (r *Runtime) Wait() error {
	if r == nil {
		return errors.New("nil runtime")
	}
	<-r.done
	return r.err
}

// Done closes after all currently supported services have exited.
func (r *Runtime) Done() <-chan struct{} {
	return r.done
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
	for _, namespace := range []string{"mnt", "uts"} {
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
	if len(cfg.Modules) != 0 || cfg.Root != nil || len(cfg.Interfaces) != 0 || cfg.ResolvConf.Present || cfg.Hosts.Present {
		return errors.New("module loading, root switching, networking, and system file writes are not implemented yet")
	}
	if len(cfg.Services) > 1 {
		return errors.New("the current boot path supports at most one service")
	}
	if len(cfg.Services) == 1 && cfg.Services[0].Restart != "never" {
		return errors.New("service restart policies are not implemented yet")
	}
	return nil
}

func run(ctx context.Context, cfg *Config, logger *slog.Logger, closer io.Closer, system bootSystem) (*Runtime, error) {
	if err := system.MakeMountsPrivate(); err != nil {
		return nil, fmt.Errorf("make mount propagation private: %w", err)
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
	runtime := &Runtime{done: make(chan struct{})}
	if len(cfg.Services) == 0 {
		logger.Info("boot setup complete")
		closer.Close()
		close(runtime.done)
		return runtime, nil
	}
	service := cfg.Services[0]
	logger.Info("starting service", "service", service.Name, "command", service.Command[0])
	process, err := system.StartService(ctx, service, serviceEnvironment(cfg.Environment, service.Environment))
	if err != nil {
		return nil, fmt.Errorf("start service %q: %w", service.Name, err)
	}
	go func() {
		if err := process.Wait(); err != nil {
			runtime.err = fmt.Errorf("service %q: %w", service.Name, err)
			logger.Error("service exited", "service", service.Name, "error", err)
		} else {
			logger.Info("service exited", "service", service.Name)
		}
		closer.Close()
		close(runtime.done)
	}()
	return runtime, nil
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
