package goboot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"
)

const serviceStopGrace = 5 * time.Second
const firstRestartDelay = 250 * time.Millisecond
const maximumRestartDelay = 30 * time.Second
const restartDelayResetUptime = 30 * time.Second

// Runtime tracks services started by Run. It does not own the caller's process.
type Runtime struct {
	done    chan struct{}
	cancel  context.CancelFunc
	mu      sync.Mutex
	err     error
	managed map[int]bool
}

// Wait blocks until all configured services have stopped. It can be called
// more than once. A service that exits unsuccessfully without a restart policy
// stops the other managed services and is returned as an error.
func (r *Runtime) Wait() error {
	if r == nil {
		return errors.New("nil runtime")
	}
	<-r.done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Done closes after all configured services have stopped.
func (r *Runtime) Done() <-chan struct{} { return r.done }

// Stop requests graceful shutdown of managed services, then waits for them.
// The importing program remains in control of its own lifetime and children.
func (r *Runtime) Stop() error {
	if r == nil {
		return errors.New("nil runtime")
	}
	r.cancel()
	return r.Wait()
}

func (r *Runtime) fail(err error) {
	r.mu.Lock()
	r.err = errors.Join(r.err, err)
	r.mu.Unlock()
	r.cancel()
}

func (r *Runtime) manage(process serviceProcess) {
	if pid := process.PID(); pid > 0 {
		r.mu.Lock()
		r.managed[pid] = true
		r.mu.Unlock()
	}
}

func (r *Runtime) unmanage(process serviceProcess) {
	if pid := process.PID(); pid > 0 {
		r.mu.Lock()
		delete(r.managed, pid)
		r.mu.Unlock()
	}
}

func (r *Runtime) manages(pid int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.managed[pid]
}

type runningService struct {
	config      ServiceConfig
	environment []string
	process     serviceProcess
}

func startServices(ctx context.Context, cfg *Config, logger *slog.Logger, closer io.Closer, system bootSystem) (*Runtime, error) {
	serviceCtx, cancel := context.WithCancel(ctx)
	runtime := &Runtime{done: make(chan struct{}), cancel: cancel, managed: make(map[int]bool)}
	if len(cfg.Services) == 0 {
		logger.Info("boot setup complete")
		closer.Close()
		close(runtime.done)
		cancel()
		return runtime, nil
	}
	running := make([]runningService, 0, len(cfg.Services))
	for _, service := range cfg.Services {
		environment := serviceEnvironment(cfg.Environment, service.Environment)
		logger.Info("starting service", "service", service.Name, "command", service.Command[0])
		process, err := system.StartService(service, environment)
		if err != nil {
			cancel()
			for _, started := range running {
				started.process.Kill()
				started.process.Wait()
			}
			return nil, fmt.Errorf("start service %q: %w", service.Name, err)
		}
		running = append(running, runningService{config: service, environment: environment, process: process})
		runtime.manage(process)
	}
	var group sync.WaitGroup
	group.Add(len(running))
	for _, service := range running {
		go func() {
			defer group.Done()
			runtime.supervise(serviceCtx, service, logger, system)
		}()
	}
	go func() {
		group.Wait()
		cancel()
		closer.Close()
		close(runtime.done)
	}()
	return runtime, nil
}

func (r *Runtime) supervise(ctx context.Context, service runningService, logger *slog.Logger, system bootSystem) {
	process := service.process
	var previousDelay time.Duration
	for {
		started := time.Now()
		exitErr, stopping, stopErr := waitManagedProcess(ctx, process)
		r.unmanage(process)
		if stopping {
			if stopErr != nil {
				r.fail(fmt.Errorf("stop service %q: %w", service.config.Name, stopErr))
			}
			return
		}
		if exitErr != nil {
			logger.Error("service exited", "service", service.config.Name, "error", exitErr)
		} else {
			logger.Info("service exited", "service", service.config.Name)
		}
		shouldRestart := service.config.Restart == "always" || service.config.Restart == "on-failure" && exitErr != nil
		if !shouldRestart {
			if exitErr != nil {
				r.fail(fmt.Errorf("service %q: %w", service.config.Name, exitErr))
			}
			return
		}
		previousDelay = nextRestartDelay(previousDelay, time.Since(started))
		logger.Warn("restarting service", "service", service.config.Name, "delay", previousDelay)
		timer := time.NewTimer(previousDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		var err error
		r.mu.Lock()
		process, err = system.StartService(service.config, service.environment)
		if err == nil && process.PID() > 0 {
			r.managed[process.PID()] = true
		}
		r.mu.Unlock()
		if err != nil {
			r.fail(fmt.Errorf("restart service %q: %w", service.config.Name, err))
			return
		}
		logger.Info("service restarted", "service", service.config.Name)
	}
}

func nextRestartDelay(previous, uptime time.Duration) time.Duration {
	if uptime >= restartDelayResetUptime || previous == 0 {
		return firstRestartDelay
	}
	if previous >= maximumRestartDelay/2 {
		return maximumRestartDelay
	}
	return previous * 2
}

func waitManagedProcess(ctx context.Context, process serviceProcess) (exitErr error, stopping bool, stopErr error) {
	return waitManagedProcessWithGrace(ctx, process, serviceStopGrace)
}

func waitManagedProcessWithGrace(ctx context.Context, process serviceProcess, grace time.Duration) (exitErr error, stopping bool, stopErr error) {
	waited := make(chan error, 1)
	go func() { waited <- process.Wait() }()
	select {
	case err := <-waited:
		return err, false, nil
	case <-ctx.Done():
	}
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		stopErr = fmt.Errorf("signal termination: %w", err)
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-waited:
		return nil, true, stopErr
	case <-timer.C:
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		stopErr = errors.Join(stopErr, fmt.Errorf("force-stop: %w", err))
	}
	<-waited
	return nil, true, stopErr
}
