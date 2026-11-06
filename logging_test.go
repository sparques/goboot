package goboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenLoggerToConfiguredFile(t *testing.T) {
	if _, _, err := OpenLogger("relative.log"); err == nil {
		t.Fatal("relative log path accepted")
	}
	path := filepath.Join(t.TempDir(), "boot.log")
	logger, closer, err := OpenLogger(path)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("boot step", "name", "mounts")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "boot step") || !strings.Contains(string(data), "name=mounts") {
		t.Fatalf("log = %q", data)
	}
}
