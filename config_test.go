package goboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDefaultsAndOptionalFiles(t *testing.T) {
	cfg, err := Parse([]byte("version: 1\nservices:\n  - name: app\n    command: [/bin/app]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.Output != "/dev/console" || cfg.OnExit != "wait" || cfg.Services[0].Restart != "never" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.Hosts.Present || cfg.ResolvConf.Present {
		t.Fatal("omitted files must remain absent")
	}

	cfg, err = Parse([]byte("version: 1\nlogging:\n  output: /tmp/goboot.log\nhosts: ''\nresolv_conf: ''\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.Output != "/tmp/goboot.log" || !cfg.Hosts.Present || !cfg.ResolvConf.Present || cfg.Hosts.Value != "" {
		t.Fatalf("unexpected explicit values: %+v", cfg)
	}
}

func TestParseRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{"version", "version: 2\n", "version must be 1"},
		{"unknown field", "version: 1\nunknown: true\n", "field unknown not found"},
		{"multiple documents", "version: 1\n---\nversion: 1\n", "exactly one"},
		{"relative log", "version: 1\nlogging:\n  output: log.txt\n", "logging.output"},
		{"null hosts", "version: 1\nhosts: null\n", "must be a string"},
		{"bad module path", "version: 1\nmodules: [../escape.ko]\n", "modules[0]"},
		{"module path with metadata separator", "version: 1\nmodules: ['drivers/bad:module.ko']\n", "modules[0]"},
		{"current root target", "version: 1\nroot: {source: /dev/vda, target: /, type: ext4}\n", "root.target"},
		{"root under provisional dev", "version: 1\nroot: {source: /dev/vda, target: /dev/newroot, type: ext4}\n", "root.target"},
		{"root under provisional proc", "version: 1\nroot: {source: /dev/vda, target: /proc/newroot, type: ext4}\n", "root.target"},
		{"unclean root source", "version: 1\nroot: {source: /dev/../vda, target: /newroot, type: ext4}\n", "root.source"},
		{"duplicate service", "version: 1\nservices:\n  - name: app\n    command: [/bin/app]\n  - name: app\n    command: [/bin/app]\n", "duplicate name"},
		{"bad network", "version: 1\ninterfaces:\n  - name: eth0\n    addresses: [not-a-cidr]\n", "addresses[0]"},
		{"gateway family mismatch", "version: 1\ninterfaces:\n  - name: eth0\n    routes: [{destination: 0.0.0.0/0, gateway: '2001:db8::1'}]\n", "family mismatch"},
		{"interface name too long", "version: 1\ninterfaces: [{name: aaaaaaaaaaaaaaaa}]\n", "invalid or duplicate name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v; want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadSelectedPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nhostname: from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want string
	}{
		{path, "from-file"},
		{"", "from-embed"},
	} {
		cfg, err := LoadSelected(tc.path, []byte("version: 1\nhostname: from-embed\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Hostname != tc.want {
			t.Fatalf("hostname = %q, want %q", cfg.Hostname, tc.want)
		}
	}
	if _, err := LoadSelected("", nil); err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestExampleConfig(t *testing.T) {
	if _, err := Load("config.example.yaml"); err != nil {
		t.Fatal(err)
	}
}
