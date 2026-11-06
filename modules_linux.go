package goboot

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const compressedModuleFlag = 4 // MODULE_INIT_COMPRESSED_FILE from linux/module.h

// KernelModuleTree returns the module directory for the running kernel.
func KernelModuleTree() (string, error) {
	var name syscall.Utsname
	if err := syscall.Uname(&name); err != nil {
		return "", fmt.Errorf("read kernel release: %w", err)
	}
	var release []byte
	for _, c := range name.Release {
		if c == 0 {
			break
		}
		release = append(release, byte(c))
	}
	return filepath.Join("/lib/modules", string(release)), nil
}

// ResolveModules returns absolute module paths in dependency-first order.
// A request may be a module name or a path relative to tree. Repeated requests
// and modules also reached through dependencies appear only once.
func ResolveModules(tree string, requests []string) ([]string, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	if tree == "" {
		var err error
		tree, err = KernelModuleTree()
		if err != nil {
			return nil, err
		}
	}
	if err := absoluteCleanPath("module_tree", tree); err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(tree, "modules.dep"))
	if err != nil {
		return nil, fmt.Errorf("open module dependencies in %q: %w", tree, err)
	}
	defer file.Close()
	deps := make(map[string][]string)
	byName := make(map[string][]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for line := 1; scanner.Scan(); line++ {
		value := strings.TrimSpace(scanner.Text())
		if value == "" {
			continue
		}
		path, tail, ok := strings.Cut(value, ":")
		if !ok || !validModulePath(path) {
			return nil, fmt.Errorf("modules.dep line %d: invalid module path or missing colon", line)
		}
		if _, exists := deps[path]; exists {
			return nil, fmt.Errorf("modules.dep line %d: duplicate module %q", line, path)
		}
		for _, dep := range strings.Fields(tail) {
			if !validModulePath(dep) {
				return nil, fmt.Errorf("modules.dep line %d: invalid dependency %q", line, dep)
			}
			deps[path] = append(deps[path], dep)
		}
		if deps[path] == nil {
			deps[path] = []string{}
		}
		name := moduleName(filepath.Base(path))
		byName[name] = append(byName[name], path)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read modules.dep: %w", err)
	}
	var roots []string
	for _, request := range requests {
		if !validModulePath(request) {
			return nil, fmt.Errorf("invalid module request %q", request)
		}
		if _, exists := deps[request]; exists {
			roots = append(roots, request)
			continue
		}
		if strings.Contains(request, "/") || hasModuleSuffix(request) {
			return nil, fmt.Errorf("module %q is absent from modules.dep", request)
		}
		matches := byName[moduleName(request)]
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("module %q is absent from modules.dep", request)
		case 1:
			roots = append(roots, matches[0])
		default:
			sort.Strings(matches)
			return nil, fmt.Errorf("module %q is ambiguous: %s", request, strings.Join(matches, ", "))
		}
	}
	sort.Strings(roots)
	state := make(map[string]uint8)
	var ordered []string
	var visit func(string) error
	visit = func(path string) error {
		switch state[path] {
		case 1:
			return fmt.Errorf("module dependency cycle at %q", path)
		case 2:
			return nil
		}
		children, exists := deps[path]
		if !exists {
			return fmt.Errorf("module dependency %q is absent from modules.dep", path)
		}
		state[path] = 1
		sort.Strings(children)
		for _, dep := range children {
			if err := visit(dep); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		}
		state[path] = 2
		ordered = append(ordered, filepath.Join(tree, path))
		return nil
	}
	for _, path := range roots {
		if err := visit(path); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func validModulePath(path string) bool {
	return path != "" && path != "." && path != ".." && !filepath.IsAbs(path) &&
		filepath.Clean(path) == path && !strings.HasPrefix(path, "../") &&
		!strings.ContainsAny(path, "\x00\n\r\t :")
}

func hasModuleSuffix(name string) bool {
	for _, suffix := range []string{".ko", ".ko.gz", ".ko.xz", ".ko.zst"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func moduleName(name string) string {
	for _, suffix := range []string{".ko.zst", ".ko.xz", ".ko.gz", ".ko"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return strings.ReplaceAll(name, "-", "_")
}

// LoadModule inserts one module through finit_module. A module already loaded
// by the kernel is treated as success. Compressed modules require kernel support
// for MODULE_INIT_COMPRESSED_FILE.
func LoadModule(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open module %q: %w", path, err)
	}
	defer file.Close()
	flags := uintptr(0)
	if strings.HasSuffix(path, ".ko.gz") || strings.HasSuffix(path, ".ko.xz") || strings.HasSuffix(path, ".ko.zst") {
		flags = compressedModuleFlag
	}
	err = unix.FinitModule(int(file.Fd()), "", int(flags))
	return moduleLoadResult(path, err)
}

func moduleLoadResult(path string, err error) error {
	if err == nil || errors.Is(err, syscall.EEXIST) {
		return nil
	}
	return fmt.Errorf("finit_module %q: %w", path, err)
}

// LoadModules resolves and inserts configured modules and their dependencies.
func LoadModules(tree string, requests []string) error {
	paths, err := ResolveModules(tree, requests)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	if tree == "" {
		tree, err = KernelModuleTree()
		if err != nil {
			return err
		}
	}
	base, err := filepath.EvalSymlinks(tree)
	if err != nil {
		return fmt.Errorf("resolve module tree %q: %w", tree, err)
	}
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve module %q: %w", path, err)
		}
		relative, err := filepath.Rel(base, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			return fmt.Errorf("module %q escapes module_tree", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat module %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("module %q is not a regular file", path)
		}
	}
	for _, path := range paths {
		if err := LoadModule(path); err != nil {
			return fmt.Errorf("load module %q: %w", path, err)
		}
	}
	return nil
}

func loadModules(tree string, requests []string, load func(string) error) error {
	paths, err := ResolveModules(tree, requests)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if err := load(path); err != nil {
			return fmt.Errorf("load module %q: %w", path, err)
		}
	}
	return nil
}
