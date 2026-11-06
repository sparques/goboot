package goboot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeBootSystem struct {
	fakeMountSystem
	events        []string
	privateError  error
	hostname      string
	hostnameError error
	service       ServiceConfig
	environment   []string
	serviceRuns   int
	process       serviceProcess
	moduleTree    string
	modules       []string
	moduleError   error
	root          MountConfig
	rootError     error
	interfaces    []InterfaceConfig
	networkError  error
	filesConfig   *Config
	filesError    error
}

type immediateProcess struct{}

func (immediateProcess) Wait() error            { return nil }
func (immediateProcess) Signal(os.Signal) error { return nil }
func (immediateProcess) Kill() error            { return nil }
func (immediateProcess) PID() int               { return 0 }

type blockingProcess struct{ done chan struct{} }

func (p blockingProcess) Wait() error {
	<-p.done
	return nil
}
func (p blockingProcess) Signal(os.Signal) error { return nil }
func (p blockingProcess) Kill() error            { return nil }
func (p blockingProcess) PID() int               { return 0 }

func (f *fakeBootSystem) MakeMountsPrivate() error {
	f.events = append(f.events, "private")
	return f.privateError
}

func (f *fakeBootSystem) LoadModules(tree string, modules []string) error {
	f.events = append(f.events, "modules")
	f.moduleTree = tree
	f.modules = append([]string(nil), modules...)
	return f.moduleError
}

func (f *fakeBootSystem) SwitchRoot(root MountConfig) error {
	f.events = append(f.events, "root")
	f.root = root
	return f.rootError
}

func (f *fakeBootSystem) SetupNetwork(interfaces []InterfaceConfig) error {
	f.events = append(f.events, "network")
	f.interfaces = interfaces
	return f.networkError
}

func (f *fakeBootSystem) WriteSystemFiles(cfg *Config) error {
	f.events = append(f.events, "files")
	f.filesConfig = cfg
	return f.filesError
}

func (f *fakeBootSystem) Mount(mount MountConfig) error {
	f.events = append(f.events, "mount")
	return f.fakeMountSystem.Mount(mount)
}

func (f *fakeBootSystem) SetHostname(name string) error {
	f.events = append(f.events, "hostname")
	f.hostname = name
	return f.hostnameError
}

func (f *fakeBootSystem) StartService(service ServiceConfig, environment []string) (serviceProcess, error) {
	f.events = append(f.events, "service")
	f.serviceRuns++
	f.service = service
	f.environment = environment
	if f.process != nil {
		return f.process, nil
	}
	return immediateProcess{}, nil
}

func TestRunOrdersMountHostnameAndService(t *testing.T) {
	cfg := &Config{
		Version:  ConfigVersion,
		Hostname: "device",
		Mounts:   []MountConfig{{Source: "tmpfs", Target: "/tmp", Type: "tmpfs"}},
		Environment: map[string]string{
			"PATH": "/custom/bin",
			"MODE": "global",
		},
		Services: []ServiceConfig{{
			Name:        "app",
			Command:     []string{"/bin/app"},
			Environment: map[string]string{"MODE": "local"},
		}},
	}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBootSystem{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runtime, err := run(context.Background(), cfg, logger, io.NopCloser(strings.NewReader("")), fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(fake.mounted) != 1 || fake.hostname != "device" || fake.serviceRuns != 1 || fake.service.Name != "app" {
		t.Fatalf("unexpected boot operations: %+v", fake)
	}
	if want := []string{"private", "mount", "hostname", "network", "service"}; !reflect.DeepEqual(fake.events, want) {
		t.Fatalf("operation order = %v, want %v", fake.events, want)
	}
	want := []string{"HOME=/", "MODE=local", "PATH=/custom/bin"}
	if !reflect.DeepEqual(fake.environment, want) {
		t.Fatalf("environment = %v, want %v", fake.environment, want)
	}
}

func TestRunStopsAfterHostnameFailure(t *testing.T) {
	cfg := &Config{Version: ConfigVersion, Hostname: "device", Mounts: []MountConfig{{Source: "tmpfs", Target: "/tmp", Type: "tmpfs"}}, Services: []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBootSystem{hostnameError: errors.New("permission denied")}
	_, err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), io.NopCloser(strings.NewReader("")), fake)
	if err == nil || !strings.Contains(err.Error(), "set hostname") || fake.serviceRuns != 0 {
		t.Fatalf("unexpected result: error=%v, service runs=%d", err, fake.serviceRuns)
	}
}

func TestRunStopsBeforeMountWhenPropagationCannotBePrivate(t *testing.T) {
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBootSystem{privateError: errors.New("permission denied")}
	_, err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), io.NopCloser(strings.NewReader("")), fake)
	if err == nil || !strings.Contains(err.Error(), "propagation private") || len(fake.mounted) != 0 {
		t.Fatalf("unexpected result: error=%v, mounts=%v", err, fake.mounted)
	}
}

func TestUnsupportedConfigRejectedBeforeBoot(t *testing.T) {
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}, Restart: "always"}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatalf("restart config rejected: %v", err)
	}
}

func TestRunSwitchesRootBeforeFinalMounts(t *testing.T) {
	root := &MountConfig{Target: "/newroot", Source: "/dev/vda", Type: "ext4"}
	cfg := &Config{Version: ConfigVersion, Root: root, Mounts: []MountConfig{{Source: "tmpfs", Target: "/tmp", Type: "tmpfs"}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBootSystem{}
	_, err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), io.NopCloser(strings.NewReader("")), fake)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"private", "root", "mount", "network"}; !reflect.DeepEqual(fake.events, want) {
		t.Fatalf("events = %v, want %v", fake.events, want)
	}
	if fake.root.Target != root.Target {
		t.Fatalf("root = %+v", fake.root)
	}
}

func TestRunConfiguresNetworkAndFilesBeforeService(t *testing.T) {
	cfg := &Config{
		Version:    ConfigVersion,
		Interfaces: []InterfaceConfig{{Name: "eth0", Addresses: []string{"192.0.2.10/24"}}},
		Hosts:      OptionalText{Present: true, Value: "127.0.0.1 localhost\n"},
		Services:   []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}}},
	}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBootSystem{}
	runtime, err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), io.NopCloser(strings.NewReader("")), fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Wait(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"private", "mount", "mount", "mount", "mount", "network", "files", "service"}; !reflect.DeepEqual(fake.events, want) {
		t.Fatalf("events = %v, want %v", fake.events, want)
	}
	if len(fake.interfaces) != 1 || fake.filesConfig != cfg {
		t.Fatalf("network or files not configured: %+v", fake)
	}
}

func TestRunStopsBeforeServiceOnNetworkFailure(t *testing.T) {
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBootSystem{networkError: errors.New("denied")}
	_, err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), io.NopCloser(strings.NewReader("")), fake)
	if err == nil || !strings.Contains(err.Error(), "set up network") || fake.serviceRuns != 0 {
		t.Fatalf("error = %v, service runs = %d", err, fake.serviceRuns)
	}
}

func TestRunLoadsModulesBeforeMounts(t *testing.T) {
	cfg := &Config{Version: ConfigVersion, ModuleTree: "/modules", Modules: []string{"b", "a"}, Mounts: []MountConfig{{Source: "tmpfs", Target: "/tmp", Type: "tmpfs"}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeBootSystem{}
	_, err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), io.NopCloser(strings.NewReader("")), fake)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"private", "modules", "mount", "network"}; !reflect.DeepEqual(fake.events, want) {
		t.Fatalf("events = %v, want %v", fake.events, want)
	}
	if fake.moduleTree != "/modules" || !reflect.DeepEqual(fake.modules, []string{"b", "a"}) {
		t.Fatalf("module request = %q %v", fake.moduleTree, fake.modules)
	}
}

func TestRunReturnsWhileServiceIsRunning(t *testing.T) {
	process := blockingProcess{done: make(chan struct{})}
	fake := &fakeBootSystem{process: process}
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), io.NopCloser(strings.NewReader("")), fake)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtime.Done():
		t.Fatal("service completed before it was released")
	default:
	}
	close(process.done)
	if err := runtime.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestRunWithoutServicesReturnsCompletedRuntime(t *testing.T) {
	fake := &fakeBootSystem{}
	cfg := &Config{Version: ConfigVersion}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := run(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), io.NopCloser(strings.NewReader("")), fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Wait(); err != nil || fake.serviceRuns != 0 {
		t.Fatalf("wait = %v, service runs = %d", err, fake.serviceRuns)
	}
}

func TestRunServiceLaunchesConfiguredExecutable(t *testing.T) {
	t.Setenv("GOBOOT_PARENT_ONLY", "secret")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "result")
	service := ServiceConfig{Name: "helper", Command: []string{executable, "-test.run=TestServiceHelperProcess"}}
	environment := serviceEnvironment(nil, map[string]string{"GOBOOT_TEST_HELPER": "1", "GOBOOT_TEST_RESULT": marker})
	process, err := (linuxBootSystem{}).StartService(service, environment)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "/|/usr/bin:/bin|" {
		t.Fatalf("helper environment and directory = %q", data)
	}
}

func TestServiceHelperProcess(t *testing.T) {
	if os.Getenv("GOBOOT_TEST_HELPER") != "1" {
		return
	}
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	value := directory + "|" + os.Getenv("PATH") + "|" + os.Getenv("GOBOOT_PARENT_ONLY")
	if err := os.WriteFile(os.Getenv("GOBOOT_TEST_RESULT"), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
