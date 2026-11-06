package goboot

import (
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

type fakeMountSystem struct {
	info       string
	noProc     bool
	created    []string
	mounted    []MountConfig
	mountFlags []uintptr
}

func (f *fakeMountSystem) MkdirAll(path string, _ os.FileMode) error {
	f.created = append(f.created, path)
	return nil
}

func (f *fakeMountSystem) ReadMountInfo() ([]byte, error) {
	if f.noProc {
		return nil, os.ErrNotExist
	}
	return []byte(f.info), nil
}

func (f *fakeMountSystem) Mount(mount MountConfig) error {
	f.mounted = append(f.mounted, mount)
	flags, err := mountFlags(mount.Flags)
	if err != nil {
		return err
	}
	f.mountFlags = append(f.mountFlags, flags)
	if mount.Target == "/proc" {
		f.noProc = false
	}
	return nil
}

func TestDefaultMountsUseConfiguredOrderAndSkipExistingDev(t *testing.T) {
	fake := &fakeMountSystem{
		noProc: true,
		info:   "1 0 0:1 / /dev rw - devtmpfs devtmpfs rw\n",
	}
	if err := setupMounts(&Config{}, fake); err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, mount := range fake.mounted {
		targets = append(targets, mount.Target)
	}
	if want := []string{"/proc", "/sys", "/tmp"}; !reflect.DeepEqual(targets, want) {
		t.Fatalf("mounted = %v, want %v", targets, want)
	}
	if want := []string{"/proc", "/dev", "/sys", "/tmp"}; !reflect.DeepEqual(fake.created, want) {
		t.Fatalf("created = %v, want %v", fake.created, want)
	}
}

func TestExplicitMountsReplaceDefaults(t *testing.T) {
	fake := &fakeMountSystem{}
	cfg := &Config{Mounts: []MountConfig{{Source: "tmpfs", Target: "/var/run", Type: "tmpfs", Flags: []string{"nosuid", "nodev"}}}}
	if err := setupMounts(cfg, fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.mounted) != 1 || fake.mounted[0].Target != "/var/run" {
		t.Fatalf("unexpected mounts: %+v", fake.mounted)
	}
	if fake.mountFlags[0] != syscall.MS_NOSUID|syscall.MS_NODEV {
		t.Fatalf("mount flags = %#x", fake.mountFlags[0])
	}
}

func TestExistingMountConflict(t *testing.T) {
	fake := &fakeMountSystem{info: "1 0 0:1 / /tmp rw - ext4 /dev/sda1 rw\n"}
	cfg := &Config{Mounts: []MountConfig{{Source: "tmpfs", Target: "/tmp", Type: "tmpfs"}}}
	err := setupMounts(cfg, fake)
	if err == nil || !strings.Contains(err.Error(), "already contains") {
		t.Fatalf("error = %v", err)
	}
	if len(fake.mounted) != 0 {
		t.Fatalf("mounted over an existing filesystem: %+v", fake.mounted)
	}
}

func TestExistingMountReuseAndRemount(t *testing.T) {
	info := "1 0 0:1 / /tmp rw - tmpfs tmpfs rw\n"
	fake := &fakeMountSystem{info: info}
	cfg := &Config{Mounts: []MountConfig{{Source: "tmpfs", Target: "/tmp", Type: "tmpfs"}}}
	if err := setupMounts(cfg, fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.mounted) != 0 {
		t.Fatalf("identical existing mount was repeated: %+v", fake.mounted)
	}
	cfg.Mounts[0].Flags = []string{"remount", "readonly"}
	if err := setupMounts(cfg, fake); err != nil {
		t.Fatal(err)
	}
	if len(fake.mounted) != 1 || fake.mountFlags[0] != syscall.MS_REMOUNT|syscall.MS_RDONLY {
		t.Fatalf("remount not applied: %+v %#v", fake.mounted, fake.mountFlags)
	}
}

func TestMountInfoEscapedTarget(t *testing.T) {
	found, source, fsType, err := mountedAt([]byte("1 0 0:1 / /a\\040b rw - tmpfs tmpfs rw\n"), "/a b")
	if err != nil || !found || source != "tmpfs" || fsType != "tmpfs" {
		t.Fatalf("mountedAt = %v, %q, %q, %v", found, source, fsType, err)
	}
	if _, _, _, err := mountedAt([]byte("broken\n"), "/tmp"); err == nil {
		t.Fatal("malformed mountinfo accepted")
	}
}
