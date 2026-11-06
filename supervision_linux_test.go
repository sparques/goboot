package goboot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type resultProcess struct{ err error }

func (p resultProcess) Wait() error            { return p.err }
func (p resultProcess) Signal(os.Signal) error { return nil }
func (p resultProcess) Kill() error            { return nil }
func (p resultProcess) PID() int               { return 0 }

type stoppableProcess struct {
	done    chan struct{}
	once    sync.Once
	signals atomic.Int32
}

type stubbornProcess struct {
	done  chan struct{}
	kills atomic.Int32
}

func (p *stubbornProcess) Wait() error {
	<-p.done
	return nil
}
func (p *stubbornProcess) Signal(os.Signal) error { return nil }
func (p *stubbornProcess) Kill() error {
	p.kills.Add(1)
	close(p.done)
	return nil
}
func (p *stubbornProcess) PID() int { return 0 }

func newStoppableProcess() *stoppableProcess { return &stoppableProcess{done: make(chan struct{})} }

func (p *stoppableProcess) Wait() error {
	<-p.done
	return nil
}

func (p *stoppableProcess) Signal(os.Signal) error {
	p.signals.Add(1)
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *stoppableProcess) Kill() error {
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *stoppableProcess) PID() int { return 0 }

type scriptedBootSystem struct {
	*fakeBootSystem
	mu        sync.Mutex
	processes []serviceProcess
	started   chan string
}

func (s *scriptedBootSystem) StartService(service ServiceConfig, _ []string) (serviceProcess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.processes) == 0 {
		return nil, errors.New("no scripted process")
	}
	process := s.processes[0]
	s.processes = s.processes[1:]
	s.started <- service.Name
	return process, nil
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestMultipleServicesStartInConfigOrder(t *testing.T) {
	system := &scriptedBootSystem{fakeBootSystem: &fakeBootSystem{}, processes: []serviceProcess{resultProcess{}, resultProcess{}}, started: make(chan string, 2)}
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "first", Command: []string{"/bin/first"}}, {Name: "second", Command: []string{"/bin/second"}}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := run(context.Background(), cfg, testLogger(), io.NopCloser(strings.NewReader("")), system)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Wait(); err != nil {
		t.Fatal(err)
	}
	if first, second := <-system.started, <-system.started; first != "first" || second != "second" {
		t.Fatalf("startup order = %q, %q", first, second)
	}
}

func TestRestartOnFailureThenComplete(t *testing.T) {
	system := &scriptedBootSystem{fakeBootSystem: &fakeBootSystem{}, processes: []serviceProcess{resultProcess{err: errors.New("exit status 1")}, resultProcess{}}, started: make(chan string, 2)}
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}, Restart: "on-failure"}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := run(context.Background(), cfg, testLogger(), io.NopCloser(strings.NewReader("")), system)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Wait(); err != nil {
		t.Fatal(err)
	}
	if len(system.started) != 2 {
		t.Fatalf("start count = %d, want 2", len(system.started))
	}
}

func TestAlwaysRestartThenStop(t *testing.T) {
	second := newStoppableProcess()
	system := &scriptedBootSystem{fakeBootSystem: &fakeBootSystem{}, processes: []serviceProcess{resultProcess{}, second}, started: make(chan string, 2)}
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "app", Command: []string{"/bin/app"}, Restart: "always"}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := run(context.Background(), cfg, testLogger(), io.NopCloser(strings.NewReader("")), system)
	if err != nil {
		t.Fatal(err)
	}
	<-system.started
	select {
	case <-system.started:
	case <-time.After(2 * time.Second):
		t.Fatal("service was not restarted")
	}
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
	if second.signals.Load() != 1 {
		t.Fatalf("termination signals = %d", second.signals.Load())
	}
}

func TestUnrecoverableServiceFailureStopsPeers(t *testing.T) {
	peer := newStoppableProcess()
	system := &scriptedBootSystem{fakeBootSystem: &fakeBootSystem{}, processes: []serviceProcess{resultProcess{err: errors.New("exit status 2")}, peer}, started: make(chan string, 2)}
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "first", Command: []string{"/bin/first"}}, {Name: "peer", Command: []string{"/bin/peer"}}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	runtime, err := run(context.Background(), cfg, testLogger(), io.NopCloser(strings.NewReader("")), system)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Wait(); err == nil || !strings.Contains(err.Error(), "first") {
		t.Fatalf("error = %v", err)
	}
	if peer.signals.Load() != 1 {
		t.Fatalf("peer termination signals = %d", peer.signals.Load())
	}
}

func TestRestartDelayBoundsAndReset(t *testing.T) {
	if got := nextRestartDelay(0, 0); got != firstRestartDelay {
		t.Fatalf("first delay = %s", got)
	}
	if got := nextRestartDelay(firstRestartDelay, 0); got != 2*firstRestartDelay {
		t.Fatalf("second delay = %s", got)
	}
	if got := nextRestartDelay(maximumRestartDelay, 0); got != maximumRestartDelay {
		t.Fatalf("maximum delay = %s", got)
	}
	if got := nextRestartDelay(maximumRestartDelay, restartDelayResetUptime); got != firstRestartDelay {
		t.Fatalf("reset delay = %s", got)
	}
}

func TestShutdownForceStopsAfterGracePeriod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	process := &stubbornProcess{done: make(chan struct{})}
	_, stopping, err := waitManagedProcessWithGrace(ctx, process, 10*time.Millisecond)
	if err != nil || !stopping || process.kills.Load() != 1 {
		t.Fatalf("stopping=%v, error=%v, kills=%d", stopping, err, process.kills.Load())
	}
}

func TestStartupFailureStopsEarlierService(t *testing.T) {
	first := newStoppableProcess()
	system := &scriptedBootSystem{fakeBootSystem: &fakeBootSystem{}, processes: []serviceProcess{first}, started: make(chan string, 2)}
	cfg := &Config{Version: ConfigVersion, Services: []ServiceConfig{{Name: "first", Command: []string{"/bin/first"}}, {Name: "second", Command: []string{"/bin/second"}}}}
	if err := validateRunnable(cfg); err != nil {
		t.Fatal(err)
	}
	_, err := run(context.Background(), cfg, testLogger(), io.NopCloser(strings.NewReader("")), system)
	if err == nil || !strings.Contains(err.Error(), "second") {
		t.Fatalf("error = %v", err)
	}
	select {
	case <-first.done:
	default:
		t.Fatal("earlier service was left running")
	}
}
