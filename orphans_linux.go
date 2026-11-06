package goboot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ReapOrphans periodically reaps exited PID 1 children that this Runtime does
// not manage as services. It requires /proc. Importing PID 1 programs should
// call it only if they want GoBoot to reap all their other exited children.
func (r *Runtime) ReapOrphans(ctx context.Context) error {
	if r == nil {
		return errors.New("nil runtime")
	}
	if os.Getpid() != 1 {
		return errors.New("orphan reaping requires PID 1")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	childSignals := make(chan os.Signal, 1)
	signal.Notify(childSignals, syscall.SIGCHLD)
	defer signal.Stop(childSignals)
	for {
		if _, err := r.reapOrphansOnce(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-childSignals:
		case <-ticker.C:
		}
	}
}

func (r *Runtime) reapOrphansOnce() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("read /proc for orphan reaping: %w", err)
	}
	count := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || r.manages(pid) {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return count, fmt.Errorf("read process %d status: %w", pid, err)
		}
		if !zombieChildOfPID1(data) || r.manages(pid) {
			continue
		}
		var status syscall.WaitStatus
		reaped, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.ECHILD) || errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return count, fmt.Errorf("reap orphan %d: %w", pid, err)
		}
		if reaped > 0 {
			count++
		}
	}
	return count, nil
}

func zombieChildOfPID1(status []byte) bool {
	zombie, child := false, false
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "State:":
			zombie = fields[1] == "Z"
		case "PPid:":
			child = fields[1] == "1"
		}
	}
	return zombie && child
}
