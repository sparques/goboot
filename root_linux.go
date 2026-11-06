package goboot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const oldRootDirectory = ".goboot-old-root"

type rootSystem interface {
	mountSystem
	PivotRoot(newRoot, putOld string) error
	Chdir(path string) error
	Chroot(path string) error
	MoveMount(source, target string) error
	Unmount(path string, flags int) error
	Remove(path string) error
}

type linuxRootSystem struct{ linuxMountSystem }

func (linuxRootSystem) PivotRoot(newRoot, putOld string) error {
	return PivotRoot(newRoot, putOld)
}

func (linuxRootSystem) Chdir(path string) error { return os.Chdir(path) }

func (linuxRootSystem) Chroot(path string) error { return Chroot(path) }

func (linuxRootSystem) MoveMount(source, target string) error { return MoveMount(source, target) }

func (linuxRootSystem) Unmount(path string, flags int) error {
	return Unmount(path, flags)
}

func (linuxRootSystem) Remove(path string) error { return os.Remove(path) }

// PivotRoot swaps the process mount namespace's root with newRoot.
func PivotRoot(newRoot, putOld string) error { return unix.PivotRoot(newRoot, putOld) }

// Chroot changes the process filesystem root.
func Chroot(path string) error { return syscall.Chroot(path) }

// MoveMount moves an existing mount to another mount point.
func MoveMount(source, target string) error {
	return syscall.Mount(source, target, "", syscall.MS_MOVE, "")
}

// Unmount removes a mount with the supplied Linux unmount flags.
func Unmount(path string, flags int) error { return syscall.Unmount(path, flags) }

// SwitchRoot mounts root and switches into it. A regular old root is detached;
// initramfs rootfs is overmounted because the kernel cannot pivot it.
// The caller must first make mount propagation private. root.Target names a
// directory in the initial root; later mount targets refer to the new root.
func SwitchRoot(root MountConfig) error {
	return switchRoot(root, linuxRootSystem{})
}

func switchRoot(root MountConfig, system rootSystem) error {
	if err := validateMount("root", root); err != nil {
		return err
	}
	if root.Target == "/" {
		return fmt.Errorf("root.target: use an omitted root to keep the current root")
	}
	// mountinfo lives in procfs. An initramfs may not have mounted /proc yet.
	provisionalProc := false
	if _, err := system.ReadMountInfo(); os.IsNotExist(err) {
		if err := system.MkdirAll("/proc", 0755); err != nil {
			return fmt.Errorf("create provisional /proc: %w", err)
		}
		if err := system.Mount(MountConfig{Source: "proc", Target: "/proc", Type: "proc"}); err != nil {
			return fmt.Errorf("mount provisional /proc: %w", err)
		}
		provisionalProc = true
	} else if err != nil {
		return fmt.Errorf("read mount table: %w", err)
	}
	provisionalDev := false
	if strings.HasPrefix(root.Source, "/dev/") {
		info, err := system.ReadMountInfo()
		if err != nil {
			return fmt.Errorf("inspect /dev mount: %w", err)
		}
		mounted, _, _, err := mountedAt(info, "/dev")
		if err != nil {
			return fmt.Errorf("inspect /dev mount: %w", err)
		}
		if !mounted {
			if err := system.MkdirAll("/dev", 0755); err != nil {
				return fmt.Errorf("create provisional /dev: %w", err)
			}
			if err := system.Mount(MountConfig{Source: "devtmpfs", Target: "/dev", Type: "devtmpfs"}); err != nil {
				return fmt.Errorf("mount provisional /dev: %w", err)
			}
			provisionalDev = true
		}
	}
	if err := setupMounts(&Config{Mounts: []MountConfig{root}}, system); err != nil {
		return fmt.Errorf("mount new root: %w", err)
	}
	info, err := system.ReadMountInfo()
	if err != nil {
		return fmt.Errorf("inspect current root: %w", err)
	}
	mounted, _, fsType, err := mountedAt(info, "/")
	if err != nil {
		return fmt.Errorf("inspect current root: %w", err)
	}
	if !mounted {
		return fmt.Errorf("current root is absent from mount table")
	}
	if provisionalDev {
		if err := system.Unmount("/dev", syscall.MNT_DETACH); err != nil {
			return fmt.Errorf("detach provisional /dev: %w", err)
		}
	}
	if provisionalProc {
		if err := system.Unmount("/proc", syscall.MNT_DETACH); err != nil {
			return fmt.Errorf("detach provisional /proc: %w", err)
		}
	}
	if fsType == "rootfs" {
		if err := system.Chdir(root.Target); err != nil {
			return fmt.Errorf("change to new root %q: %w", root.Target, err)
		}
		if err := system.MoveMount(".", "/"); err != nil {
			return fmt.Errorf("move new root over rootfs: %w", err)
		}
		if err := system.Chroot("."); err != nil {
			return fmt.Errorf("chroot to new root: %w", err)
		}
		if err := system.Chdir("/"); err != nil {
			return fmt.Errorf("change to new root: %w", err)
		}
		return nil
	}
	putOld := filepath.Join(root.Target, oldRootDirectory)
	if err := system.MkdirAll(putOld, 0700); err != nil {
		return fmt.Errorf("create old-root mount point %q: %w", putOld, err)
	}
	if err := system.PivotRoot(root.Target, putOld); err != nil {
		return fmt.Errorf("pivot_root %q: %w", root.Target, err)
	}
	if err := system.Chdir("/"); err != nil {
		return fmt.Errorf("change to new root: %w", err)
	}
	oldRoot := "/" + oldRootDirectory
	if err := system.Unmount(oldRoot, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root %q: %w", oldRoot, err)
	}
	if err := system.Remove(oldRoot); err != nil {
		return fmt.Errorf("remove old-root mount point %q: %w", oldRoot, err)
	}
	return nil
}
