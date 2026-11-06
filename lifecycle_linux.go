package goboot

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"
)

// RebootAction selects a Linux reboot syscall operation.
type RebootAction string

const (
	Restart  RebootAction = "reboot"
	PowerOff RebootAction = "poweroff"
)

// SyncFilesystems flushes pending filesystem writes.
func SyncFilesystems() {
	syscall.Sync()
}

// Reboot syncs filesystems and requests a restart or poweroff. Callers must
// ensure their services have stopped before invoking it.
func Reboot(action RebootAction) error {
	var command int
	switch action {
	case Restart:
		command = syscall.LINUX_REBOOT_CMD_RESTART
	case PowerOff:
		command = syscall.LINUX_REBOOT_CMD_POWER_OFF
	default:
		return fmt.Errorf("invalid reboot action %q", action)
	}
	SyncFilesystems()
	if err := syscall.Reboot(command); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s returned unexpectedly", action)
}

// ReapChildren reaps children that have already exited and returns their count.
// Do not call it while another goroutine is waiting on the same child.
func ReapChildren() (int, error) {
	count := 0
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid > 0 {
			count++
			continue
		}
		if err == nil || errors.Is(err, syscall.ECHILD) || errors.Is(err, syscall.EINTR) {
			return count, nil
		}
		return count, fmt.Errorf("reap child: %w", err)
	}
}

// WaitForChildren keeps PID 1 alive while reaping exited orphan children.
// Call it only after separately waited services are finished.
func WaitForChildren(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := ReapChildren(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
