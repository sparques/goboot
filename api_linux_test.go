package goboot_test

import (
	"testing"

	"github.com/sparques/goboot"
)

func TestExportedOperationsValidateBeforeSyscalls(t *testing.T) {
	if err := goboot.Mount(goboot.MountConfig{}); err == nil {
		t.Fatal("invalid mount reached syscall")
	}
	if err := goboot.SetHostname(""); err == nil {
		t.Fatal("invalid hostname reached syscall")
	}
	if err := goboot.Reboot("invalid"); err == nil {
		t.Fatal("invalid reboot action reached syscall")
	}
}
