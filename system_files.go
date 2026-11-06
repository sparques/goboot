package goboot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteSystemFiles atomically replaces /etc/resolv.conf and /etc/hosts only
// when their corresponding config fields are present. Call it after switching
// to the final root.
func WriteSystemFiles(cfg *Config) error {
	return writeSystemFilesAt("/", cfg)
}

func writeSystemFilesAt(root string, cfg *Config) error {
	if cfg == nil {
		return errors.New("nil config")
	}
	for _, file := range []struct {
		name string
		text OptionalText
	}{
		{"resolv.conf", cfg.ResolvConf},
		{"hosts", cfg.Hosts},
	} {
		if !file.text.Present {
			continue
		}
		path := filepath.Join(root, "etc", file.name)
		if err := writeTextFileAtomic(path, file.text.Value); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func writeTextFileAtomic(path, content string) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".goboot-*")
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(file.Name())
		}
	}()
	if err = file.Chmod(0644); err != nil {
		return err
	}
	if _, err = file.WriteString(content); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return nil
}
