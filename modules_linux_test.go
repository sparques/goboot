package goboot

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func moduleTreeFixture(t *testing.T, metadata string) string {
	t.Helper()
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "modules.dep"), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestResolveModulesDependenciesAndDuplicates(t *testing.T) {
	tree := moduleTreeFixture(t, "drivers/a.ko.zst: drivers/b.ko.zst drivers/c.ko.zst\n"+
		"drivers/b.ko.zst: drivers/d.ko.zst\n"+
		"drivers/c.ko.zst: drivers/d.ko.zst\n"+
		"drivers/d.ko.zst:\n")
	first, err := ResolveModules(tree, []string{"a", "drivers/b.ko.zst", "a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveModules(tree, []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"drivers/d.ko.zst", "drivers/b.ko.zst", "drivers/c.ko.zst", "drivers/a.ko.zst"}
	for i := range want {
		want[i] = filepath.Join(tree, want[i])
	}
	if !reflect.DeepEqual(first, want) || !reflect.DeepEqual(second, want) {
		t.Fatalf("first = %v, second = %v, want %v", first, second, want)
	}
	var loaded []string
	if err := loadModules(tree, []string{"a", "b", "a"}, func(path string) error {
		loaded = append(loaded, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("loaded = %v, want %v", loaded, want)
	}
}

func TestResolveModulesErrors(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		requests       []string
		contains       string
	}{
		{"missing request", "a.ko:\n", []string{"missing"}, "absent"},
		{"missing dependency", "a.ko: b.ko\n", []string{"a"}, "dependency \"b.ko\" is absent"},
		{"ambiguous", "one/a.ko:\ntwo/a.ko:\n", []string{"a"}, "ambiguous"},
		{"cycle", "a.ko: b.ko\nb.ko: a.ko\n", []string{"a"}, "cycle"},
		{"invalid path", "../a.ko:\n", []string{"a"}, "invalid module path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := moduleTreeFixture(t, tc.metadata)
			_, err := ResolveModules(tree, tc.requests)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error = %v, want %q", err, tc.contains)
			}
		})
	}
}

func TestLoadModulesReportsModuleError(t *testing.T) {
	tree := moduleTreeFixture(t, "a.ko:\n")
	sentinel := errors.New("permission denied")
	err := loadModules(tree, []string{"a"}, func(string) error { return sentinel })
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "a.ko") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadModulesRejectsEscapingSymlink(t *testing.T) {
	tree := moduleTreeFixture(t, "a.ko:\n")
	outside := filepath.Join(t.TempDir(), "outside.ko")
	if err := os.WriteFile(outside, []byte("module"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tree, "a.ko")); err != nil {
		t.Fatal(err)
	}
	if err := LoadModules(tree, []string{"a"}); err == nil || !strings.Contains(err.Error(), "escapes module_tree") {
		t.Fatalf("error = %v", err)
	}
}

func TestAlreadyLoadedModuleIsAccepted(t *testing.T) {
	if err := moduleLoadResult("a.ko", syscall.EEXIST); err != nil {
		t.Fatal(err)
	}
}
