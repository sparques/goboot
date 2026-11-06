package goboot

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const ConfigVersion = 1
const DefaultLogOutput = "/dev/console"

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// OptionalText distinguishes an omitted YAML field from an explicitly empty one.
type OptionalText struct {
	Present bool
	Value   string
}

func (t *OptionalText) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return errors.New("must be a string; use an empty string to write an empty file")
	}
	t.Present = true
	t.Value = node.Value
	return nil
}

type Config struct {
	Version     int               `yaml:"version"`
	Logging     LoggingConfig     `yaml:"logging"`
	ModuleTree  string            `yaml:"module_tree"`
	Modules     []string          `yaml:"modules"`
	Mounts      []MountConfig     `yaml:"mounts"`
	Root        *MountConfig      `yaml:"root"`
	Hostname    string            `yaml:"hostname"`
	Interfaces  []InterfaceConfig `yaml:"interfaces"`
	ResolvConf  OptionalText      `yaml:"resolv_conf"`
	Hosts       OptionalText      `yaml:"hosts"`
	Environment map[string]string `yaml:"environment"`
	Services    []ServiceConfig   `yaml:"services"`
	OnExit      string            `yaml:"on_exit"`
}

type LoggingConfig struct {
	Output string `yaml:"output"`
}

type MountConfig struct {
	Source  string   `yaml:"source"`
	Target  string   `yaml:"target"`
	Type    string   `yaml:"type"`
	Flags   []string `yaml:"flags"`
	Options string   `yaml:"options"`
}

type InterfaceConfig struct {
	Name      string        `yaml:"name"`
	Addresses []string      `yaml:"addresses"`
	Routes    []RouteConfig `yaml:"routes"`
}

type RouteConfig struct {
	Destination string `yaml:"destination"`
	Gateway     string `yaml:"gateway"`
}

type ServiceConfig struct {
	Name        string            `yaml:"name"`
	Command     []string          `yaml:"command"`
	Directory   string            `yaml:"directory"`
	Environment map[string]string `yaml:"environment"`
	Restart     string            `yaml:"restart"`
}

// Parse accepts exactly one YAML document and rejects unknown fields.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("config must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// yaml.v3 does not call a value's UnmarshalYAML method for a null scalar.
	// Check these fields separately so null cannot masquerade as omission.
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(document.Content) != 0 {
		fields := document.Content[0].Content
		for i := 0; i+1 < len(fields); i += 2 {
			if (fields[i].Value == "hosts" || fields[i].Value == "resolv_conf") && fields[i+1].Tag == "!!null" {
				return nil, fmt.Errorf("%s: must be a string; use an empty string to write an empty file", fields[i].Value)
			}
		}
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Load reads and validates a YAML file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	return Parse(data)
}

// LoadSelected uses an explicit file when supplied, otherwise embedded YAML.
func LoadSelected(path string, embedded []byte) (*Config, error) {
	if path != "" {
		return Load(path)
	}
	if len(bytes.TrimSpace(embedded)) == 0 {
		return nil, errors.New("no config: provide -config or embed YAML at build time")
	}
	return Parse(embedded)
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("nil config")
	}
	if c.Version != ConfigVersion {
		return fmt.Errorf("version must be %d", ConfigVersion)
	}
	if c.Logging.Output == "" {
		c.Logging.Output = DefaultLogOutput
	}
	if c.Logging.Output != "stdout" && c.Logging.Output != "stderr" {
		if err := absoluteCleanPath("logging.output", c.Logging.Output); err != nil {
			return err
		}
	}
	if c.ModuleTree != "" {
		if err := absoluteCleanPath("module_tree", c.ModuleTree); err != nil {
			return err
		}
	}
	for i, module := range c.Modules {
		if module == "" || filepath.Clean(module) != module || module == ".." || strings.HasPrefix(module, "../") || strings.ContainsAny(module, "\x00\n") {
			return fmt.Errorf("modules[%d]: invalid module name or path", i)
		}
		if filepath.IsAbs(module) {
			return fmt.Errorf("modules[%d]: use a name or path relative to module_tree", i)
		}
	}
	for i, mount := range c.Mounts {
		if err := validateMount(fmt.Sprintf("mounts[%d]", i), mount); err != nil {
			return err
		}
	}
	if c.Root != nil {
		if err := validateMount("root", *c.Root); err != nil {
			return err
		}
		if c.Root.Target == "/" {
			return errors.New("root.target: omit root to keep the current root")
		}
	}
	if len(c.Hostname) > 64 || strings.ContainsAny(c.Hostname, "\x00\n") {
		return errors.New("hostname: must be at most 64 bytes and contain no NUL or newline")
	}
	seenInterfaces := make(map[string]bool)
	for i, iface := range c.Interfaces {
		if iface.Name == "" || len(iface.Name) >= 16 || strings.ContainsAny(iface.Name, "/\x00\n") || seenInterfaces[iface.Name] {
			return fmt.Errorf("interfaces[%d]: invalid or duplicate name", i)
		}
		seenInterfaces[iface.Name] = true
		for j, address := range iface.Addresses {
			if _, _, err := net.ParseCIDR(address); err != nil {
				return fmt.Errorf("interfaces[%d].addresses[%d]: %w", i, j, err)
			}
		}
		for j, route := range iface.Routes {
			if _, _, err := net.ParseCIDR(route.Destination); err != nil {
				return fmt.Errorf("interfaces[%d].routes[%d].destination: %w", i, j, err)
			}
			if route.Gateway != "" {
				gateway := net.ParseIP(route.Gateway)
				_, destination, _ := net.ParseCIDR(route.Destination)
				if gateway == nil || (gateway.To4() == nil) != (destination.IP.To4() == nil) {
					return fmt.Errorf("interfaces[%d].routes[%d].gateway: invalid IP or family mismatch", i, j)
				}
			}
		}
	}
	if err := validateEnvironment("environment", c.Environment); err != nil {
		return err
	}
	seenServices := make(map[string]bool)
	for i, service := range c.Services {
		if service.Name == "" || seenServices[service.Name] {
			return fmt.Errorf("services[%d]: empty or duplicate name", i)
		}
		seenServices[service.Name] = true
		if len(service.Command) == 0 || service.Command[0] == "" {
			return fmt.Errorf("services[%d].command: executable required", i)
		}
		if err := absoluteCleanPath(fmt.Sprintf("services[%d].command[0]", i), service.Command[0]); err != nil {
			return err
		}
		if service.Directory != "" {
			if err := absoluteCleanPath(fmt.Sprintf("services[%d].directory", i), service.Directory); err != nil {
				return err
			}
		}
		if err := validateEnvironment(fmt.Sprintf("services[%d].environment", i), service.Environment); err != nil {
			return err
		}
		if service.Restart == "" {
			c.Services[i].Restart = "never"
		} else if service.Restart != "never" && service.Restart != "on-failure" && service.Restart != "always" {
			return fmt.Errorf("services[%d].restart: invalid mode %q", i, service.Restart)
		}
	}
	if c.OnExit == "" {
		c.OnExit = "wait"
	} else if c.OnExit != "wait" && c.OnExit != "reboot" && c.OnExit != "poweroff" {
		return fmt.Errorf("on_exit: invalid action %q", c.OnExit)
	}
	return nil
}

func validateMount(name string, mount MountConfig) error {
	if err := absoluteCleanPath(name+".target", mount.Target); err != nil {
		return err
	}
	if mount.Type == "" || strings.ContainsAny(mount.Type, "/\x00\n") {
		return fmt.Errorf("%s.type: filesystem type required", name)
	}
	if strings.ContainsAny(mount.Source, "\x00\n") || strings.ContainsAny(mount.Options, "\x00\n") {
		return fmt.Errorf("%s: source and options must not contain NUL or newline", name)
	}
	for _, flag := range mount.Flags {
		switch flag {
		case "readonly", "nosuid", "nodev", "noexec", "sync", "dirsync", "remount", "bind", "rbind":
		default:
			return fmt.Errorf("%s.flags: unknown flag %q", name, flag)
		}
	}
	return nil
}

func absoluteCleanPath(name, path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\n") {
		return fmt.Errorf("%s: must be a clean absolute path", name)
	}
	return nil
}

func validateEnvironment(name string, environment map[string]string) error {
	for key, value := range environment {
		if !envName.MatchString(key) || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%s: invalid variable %q", name, key)
		}
	}
	return nil
}
