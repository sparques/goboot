package goboot

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type fakeRootSystem struct {
	fakeMountSystem
	events         []string
	fail           string
	resolvedTarget string
}

func (f *fakeRootSystem) MkdirAll(path string, mode os.FileMode) error {
	f.events = append(f.events, "mkdir "+path)
	return f.fakeMountSystem.MkdirAll(path, mode)
}

func (f *fakeRootSystem) Mount(mount MountConfig) error {
	f.events = append(f.events, "mount "+mount.Target)
	return f.fakeMountSystem.Mount(mount)
}

func (f *fakeRootSystem) PivotRoot(newRoot, putOld string) error {
	f.events = append(f.events, "pivot "+newRoot+" "+putOld)
	return f.maybeFail("pivot")
}

func (f *fakeRootSystem) Chdir(path string) error {
	f.events = append(f.events, "chdir "+path)
	return f.maybeFail("chdir")
}

func (f *fakeRootSystem) Chroot(path string) error {
	f.events = append(f.events, "chroot "+path)
	return f.maybeFail("chroot")
}

func (f *fakeRootSystem) MoveMount(source, target string) error {
	f.events = append(f.events, "move "+source+" "+target)
	return f.maybeFail("move")
}

func (f *fakeRootSystem) Unmount(path string, _ int) error {
	f.events = append(f.events, "unmount "+path)
	return f.maybeFail("unmount")
}

func (f *fakeRootSystem) Remove(path string) error {
	f.events = append(f.events, "remove "+path)
	return f.maybeFail("remove")
}

func (f *fakeRootSystem) EvalSymlinks(path string) (string, error) {
	if f.resolvedTarget != "" {
		return f.resolvedTarget, nil
	}
	return path, nil
}

func (f *fakeRootSystem) maybeFail(operation string) error {
	if f.fail == operation {
		return errors.New("denied")
	}
	return nil
}

func TestSwitchRootFromInitramfs(t *testing.T) {
	fake := &fakeRootSystem{fakeMountSystem: fakeMountSystem{info: "1 0 0:1 / / rw - rootfs rootfs rw\n"}}
	root := MountConfig{Source: "/dev/vda", Target: "/newroot", Type: "ext4"}
	if err := switchRoot(root, fake); err != nil {
		t.Fatal(err)
	}
	want := []string{"mkdir /newroot", "mkdir /dev", "mount /dev", "mkdir /newroot", "mount /newroot", "unmount /dev", "chdir /newroot", "move . /", "chroot .", "chdir /"}
	if !reflect.DeepEqual(fake.events, want) {
		t.Fatalf("events = %v, want %v", fake.events, want)
	}
}

func TestSwitchRootByPivot(t *testing.T) {
	fake := &fakeRootSystem{fakeMountSystem: fakeMountSystem{info: "1 0 0:1 / / rw - ext4 /dev/vdb rw\n"}}
	root := MountConfig{Source: "/dev/vda", Target: "/newroot", Type: "ext4"}
	if err := switchRoot(root, fake); err != nil {
		t.Fatal(err)
	}
	want := []string{"mkdir /newroot", "mkdir /dev", "mount /dev", "mkdir /newroot", "mount /newroot", "unmount /dev", "mkdir /newroot/.goboot-old-root", "pivot /newroot /newroot/.goboot-old-root", "chdir /", "unmount /.goboot-old-root", "remove /.goboot-old-root"}
	if !reflect.DeepEqual(fake.events, want) {
		t.Fatalf("events = %v, want %v", fake.events, want)
	}
}

func TestSwitchRootStopsOnMoveFailure(t *testing.T) {
	fake := &fakeRootSystem{fakeMountSystem: fakeMountSystem{info: "1 0 0:1 / / rw - rootfs rootfs rw\n"}, fail: "move"}
	err := switchRoot(MountConfig{Source: "/dev/vda", Target: "/newroot", Type: "ext4"}, fake)
	if err == nil || !strings.Contains(err.Error(), "move new root") {
		t.Fatalf("error = %v", err)
	}
	if strings.Join(fake.events, ",") != "mkdir /newroot,mkdir /dev,mount /dev,mkdir /newroot,mount /newroot,unmount /dev,chdir /newroot,move . /" {
		t.Fatalf("events = %v", fake.events)
	}
}

func TestSwitchRootRejectsSymlinkedTargetBeforeMounting(t *testing.T) {
	fake := &fakeRootSystem{fakeMountSystem: fakeMountSystem{info: "1 0 0:1 / / rw - rootfs rootfs rw\n"}, resolvedTarget: "/dev/newroot"}
	err := switchRoot(MountConfig{Source: "/dev/vda", Target: "/newroot", Type: "ext4"}, fake)
	if err == nil || !strings.Contains(err.Error(), "resolves to") {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(fake.events, []string{"mkdir /newroot"}) {
		t.Fatalf("events = %v", fake.events)
	}
}
