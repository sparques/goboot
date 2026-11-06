package goboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSystemFilesPresence(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	if err := os.Mkdir(etc, 0755); err != nil {
		t.Fatal(err)
	}
	hosts := filepath.Join(etc, "hosts")
	if err := os.WriteFile(hosts, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeSystemFilesAt(root, &Config{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(hosts)
	if err != nil || string(data) != "old" {
		t.Fatalf("omitted hosts = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(etc, "resolv.conf")); !os.IsNotExist(err) {
		t.Fatalf("omitted resolver file exists: %v", err)
	}
	cfg := &Config{Hosts: OptionalText{Present: true}, ResolvConf: OptionalText{Present: true, Value: "nameserver 192.0.2.1\n"}}
	if err := writeSystemFilesAt(root, cfg); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(hosts)
	if err != nil || len(data) != 0 {
		t.Fatalf("empty hosts = %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(etc, "resolv.conf"))
	if err != nil || string(data) != cfg.ResolvConf.Value {
		t.Fatalf("resolver file = %q, %v", data, err)
	}
}

func TestWriteSystemFilesReplacesSymlink(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	if err := os.Mkdir(etc, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(etc, "hosts")); err != nil {
		t.Fatal(err)
	}
	if err := writeSystemFilesAt(root, &Config{Hosts: OptionalText{Present: true, Value: "new"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "keep" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	data, err = os.ReadFile(filepath.Join(etc, "hosts"))
	if err != nil || string(data) != "new" {
		t.Fatalf("hosts = %q, %v", data, err)
	}
}

func TestWriteSystemFilesReportsDestination(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "etc"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	err := writeSystemFilesAt(root, &Config{Hosts: OptionalText{Present: true, Value: "new"}})
	if err == nil || !strings.Contains(err.Error(), "hosts") {
		t.Fatalf("error = %v", err)
	}
}
