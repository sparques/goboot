package goboot

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type mountSystem interface {
	MkdirAll(string, os.FileMode) error
	ReadMountInfo() ([]byte, error)
	Mount(MountConfig) error
}

type linuxMountSystem struct{}

func (linuxMountSystem) MkdirAll(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (linuxMountSystem) ReadMountInfo() ([]byte, error) {
	return os.ReadFile("/proc/self/mountinfo")
}

func (linuxMountSystem) Mount(mount MountConfig) error {
	return Mount(mount)
}

// Mount applies one validated mount specification using the Linux mount syscall.
// It does not create the target directory or inspect existing mounts.
func Mount(mount MountConfig) error {
	if err := validateMount("mount", mount); err != nil {
		return err
	}
	flags, err := mountFlags(mount.Flags)
	if err != nil {
		return err
	}
	if err := syscall.Mount(mount.Source, mount.Target, mount.Type, flags, mount.Options); err != nil {
		return fmt.Errorf("mount %q (%s): %w", mount.Target, mount.Type, err)
	}
	return nil
}

func defaultMounts() []MountConfig {
	return []MountConfig{
		{Source: "proc", Target: "/proc", Type: "proc"},
		{Source: "devtmpfs", Target: "/dev", Type: "devtmpfs"},
		{Source: "sysfs", Target: "/sys", Type: "sysfs"},
		{Source: "tmpfs", Target: "/tmp", Type: "tmpfs"},
	}
}

// SetupMounts applies the configured mount list, or the four default mounts.
// It must run only in an isolated mount namespace or during early boot.
func SetupMounts(cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return setupMounts(cfg, linuxMountSystem{})
}

func setupMounts(cfg *Config, system mountSystem) error {
	mounts := cfg.Mounts
	if len(mounts) == 0 {
		mounts = defaultMounts()
	}
	for _, mount := range mounts {
		if err := system.MkdirAll(mount.Target, 0755); err != nil {
			return fmt.Errorf("create mount target %q: %w", mount.Target, err)
		}
		info, err := system.ReadMountInfo()
		if err != nil && !(mount.Target == "/proc" && os.IsNotExist(err)) {
			return fmt.Errorf("read mount table: %w", err)
		}
		if err == nil {
			mounted, source, fsType, err := mountedAt(info, mount.Target)
			if err != nil {
				return fmt.Errorf("read mount table: %w", err)
			}
			if mounted {
				if source == mount.Source && fsType == mount.Type && len(mount.Flags) == 0 && mount.Options == "" {
					continue
				}
				if !hasMountFlag(mount.Flags, "remount") {
					return fmt.Errorf("mount target %q already contains %s from %s", mount.Target, fsType, source)
				}
			} else if hasMountFlag(mount.Flags, "remount") {
				return fmt.Errorf("mount target %q is not mounted for remount", mount.Target)
			}
		}
		if err := system.Mount(mount); err != nil {
			return fmt.Errorf("mount %q (%s): %w", mount.Target, mount.Type, err)
		}
	}
	return nil
}

func hasMountFlag(flags []string, wanted string) bool {
	for _, flag := range flags {
		if flag == wanted {
			return true
		}
	}
	return false
}

func mountedAt(info []byte, target string) (bool, string, string, error) {
	var found bool
	var source, fsType string
	for _, line := range strings.Split(string(info), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			return false, "", "", errors.New("malformed mountinfo line")
		}
		before := strings.Fields(parts[0])
		after := strings.Fields(parts[1])
		if len(before) < 5 || len(after) < 2 {
			return false, "", "", errors.New("malformed mountinfo fields")
		}
		mountPoint := unescapeMountInfo(before[4])
		if mountPoint == target {
			found, source, fsType = true, unescapeMountInfo(after[1]), after[0]
		}
	}
	return found, source, fsType, nil
}

func unescapeMountInfo(value string) string {
	for _, escaped := range []string{"040", "011", "012", "134"} {
		code, _ := strconv.ParseInt(escaped, 8, 32)
		value = strings.ReplaceAll(value, "\\"+escaped, string(rune(code)))
	}
	return value
}

func mountFlags(names []string) (uintptr, error) {
	var flags uintptr
	for _, name := range names {
		switch name {
		case "readonly":
			flags |= syscall.MS_RDONLY
		case "nosuid":
			flags |= syscall.MS_NOSUID
		case "nodev":
			flags |= syscall.MS_NODEV
		case "noexec":
			flags |= syscall.MS_NOEXEC
		case "sync":
			flags |= syscall.MS_SYNCHRONOUS
		case "dirsync":
			flags |= syscall.MS_DIRSYNC
		case "remount":
			flags |= syscall.MS_REMOUNT
		case "bind":
			flags |= syscall.MS_BIND
		case "rbind":
			flags |= syscall.MS_BIND | syscall.MS_REC
		default:
			return 0, fmt.Errorf("unknown mount flag %q", name)
		}
	}
	return flags, nil
}
