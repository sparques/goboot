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
}

type immediateProcess struct{}

func (immediateProcess) Wait() error { return nil }

type blockingProcess struct{ done chan struct{} }

func (p blockingProcess) Wait() error {
	<-p.done
	return nil
}

func (f *fakeBootSystem) MakeMountsPrivate() error {
	f.events = append(f.events, "private")
	return f.privateError
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

func (f *fakeBootSystem) StartService(_ context.Context, service ServiceConfig, environment []string) (serviceProcess, error) {
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
	if want := []string{"private", "mount", "hostname", "service"}; !reflect.DeepEqual(fake.events, want) {
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
	cfg := &Config{Version: ConfigVersion, Modules: []string{"example"}, Services: []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}}}}
	if err := validateRunnable(cfg); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("unsupported config accepted: %v", err)
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
	process, err := (linuxBootSystem{}).StartService(context.Background(), service, environment)
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
