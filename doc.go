// Package goboot provides the early boot operations needed by a small Linux
// system. It can run as PID 1 from an initramfs, either through the standalone
// cmd/goboot executable or from another Go program that imports this package.
// It is intended for systems that need a focused init process rather than a
// desktop service manager. GoBoot performs its own boot setup with Go and Linux
// syscalls; it executes only the services named in its configuration.
//
// # Boot sequence
//
// GoBoot reads a versioned YAML configuration, then Run makes mount propagation
// private, loads requested kernel modules and their dependencies, and optionally
// switches to a new root filesystem. It sets up filesystems on the active root,
// sets the hostname if configured, brings up loopback and any statically
// configured interfaces, writes requested /etc/resolv.conf and /etc/hosts
// contents, and starts services in configuration order. Services run
// concurrently and can use never, on-failure, or always restart policies.
//
// An omitted root keeps the current root, typically the initramfs. When mounts
// are omitted, GoBoot sets up /proc, /dev, /sys, and /tmp. An explicit mounts
// list replaces those defaults. Kernel modules are resolved from modules.dep
// before the root switch; requests may appear in any order or more than once.
// Network settings are static; GoBoot does not run DHCP.
//
// A minimal configuration that starts one service is:
//
//	version: 1
//	services:
//	  - name: app
//	    command: [/bin/app]
//
// To mount a separate ext4 root before starting that service, add:
//
//	root: {source: /dev/vda, target: /newroot, type: ext4}
//
// The executable and any script interpreter or runtime dependencies must be
// available in the root where the service starts. Module files and their
// metadata must be available in the initial root. Mount targets, service
// commands and working directories, and the two optional /etc files refer to
// the final root. The root mount and log destination refer to the initial root.
//
// # Using goboot as a library
//
// An importing /init can load or parse configuration and call Run:
//
//	func start(ctx context.Context) (*goboot.Runtime, error) {
//	    cfg, err := goboot.Load("/etc/goboot.yaml")
//	    if err != nil {
//	        return nil, err
//	    }
//	    return goboot.Run(ctx, cfg)
//	}
//
// Run returns after setup and service startup. Runtime.Done reports when all
// managed services have stopped, Runtime.Wait returns their terminal error, and
// Runtime.Stop requests a graceful stop. The importing program owns its PID 1
// lifetime and chooses whether to wait, reboot, power off, or keep running. It
// may call Runtime.ReapOrphans to reap other adopted children while services
// run; this requires /proc on the final root. With no services, Run returns an
// already completed runtime.
//
// The package also exposes individual operations, including Mount,
// SetupMounts, LoadModules, SwitchRoot, SetupNetwork, WriteSystemFiles,
// SetHostname, and Reboot, for programs that want to assemble their own boot
// sequence. These operations act on the caller's Linux process and namespaces.
// Outside PID 1, Run requires isolated mount and network namespaces, plus an
// isolated UTS namespace if it will set a hostname.
//
// # Configuration and logging
//
// Parse validates YAML without making privileged changes. Load reads a file;
// LoadSelected chooses an explicit file over supplied embedded YAML. The
// standalone executable can embed cmd/goboot/embedded.yaml, accepts -config to
// override it, and otherwise reads /etc/goboot.yaml. Its -check option validates
// configuration without booting.
//
// Boot messages use log/slog and go to /dev/console by default. Set
// logging.output to stdout, stderr, or an absolute path in YAML to change the
// destination. GoBoot opens that destination before switching roots and keeps
// the file descriptor open afterward. The standalone executable applies the
// YAML on_exit policy after services finish; Run leaves that decision to its
// caller. On an unrecoverable boot error, the standalone PID 1 reports the
// error, remains alive, and continues reaping children.
//
// See the repository README and config.example.yaml for the complete schema,
// build instructions, and QEMU examples.
package goboot
