# goboot

GoBoot is a package and a standalone executable that replaces /sbin/init on a linux system.

It is configured through a single yaml file that may be embedded.
It is meant for embedded systems (like Raspberry Pis), thus some of the desktop boot tasks have been skipped (like keyboard setup and KMS).

GoBoot does not depend on any external binaries; it can be statically compiled and deployed with no other files needed.

All boot tasks are done directly by GoBoot by making SysCalls. 

# Tasks

All information is sourced from the yaml config.

 - Loads kernel modules per config
 - mounts filesystems per config
 	- Defaults (if no mounts are specified):
 		- /proc (proc)
 		- /dev (devtmpfs; mounts if not automatically mounted by kernel)
 		- /sys (sysfs)
 		- /tmp (tmpfs)
 	- if a root filesystem is specified that differs from what is booted; pivot to that root filesystem
 - set hostname
 - Optionally configure network devices
 	- Default: brings up lo / 127.0.0.1
 	- Other devices can be specified
 - Optionally populate /etc/resolv.conf 
 - Optionally populate /etc/hosts 
 - Runs other binaries/scripts as specified with optional restart
 - Handle orphaned children and reap zombies
 - Establish Environ
 - Depending on config, restarts programs if they exit or reboots the system.

 For /etc/resolv.conf and /etc/hosts; if not specified in the yaml, GoBoot does not do anything with those files. If they are missing, they stay missing. If they are specified, GoBoot overwrites what is already on the filesystem.

## Implementation status

The configuration parser and a limited single-service boot path are implemented.
Both the standalone executable and an importing Go `/init` boot in QEMU, check
the default mounts and hostname, run a service, and power off. General-purpose
PID 1 supervision, module loading, root switching, networking, and system file
writes remain to be built. See [plan.md](plan.md) for the remaining work.

## Go package API

An importing Go program can call `goboot.Run(ctx, config)`. It completes boot
setup, starts the configured service, and returns a `*goboot.Runtime` without
waiting for that service. The caller can continue its own `/init` work, use
`runtime.Done()` or `runtime.Wait()` to observe service exit, and choose when to
reboot or power off. `Run` does not apply `on_exit`; that YAML field is used by
the standalone executable. Zero services are valid and yield an already
completed runtime. Currently, at most one service with `restart: never` is
supported. A complete imported `/init` example is in
[`testdata/qemu/init/main.go`](testdata/qemu/init/main.go).

The package also exposes the Linux operations it currently uses:

| Function | Operation |
| --- | --- |
| `Mount(MountConfig)` | Apply one mount with the Linux mount syscall. |
| `SetupMounts(*Config)` | Create targets, select defaults, and apply mounts. |
| `MakeMountsPrivate()` | Disable mount propagation to other namespaces. |
| `SetHostname(string)` | Set the UTS hostname. |
| `SyncFilesystems()`, `Reboot(RebootAction)` | Flush writes and request restart or poweroff. |
| `ReapChildren()`, `WaitForChildren(ctx)` | Reap exited children after separately waited services have finished. |

These functions are independently callable; `Run` uses the same mount and
hostname operations. The importing program owns its PID 1 lifetime and must
keep running after `Run` returns. Additional operations will receive exported
functions as module loading, root switching, and networking are implemented.

## Configuration (version 1)

[config.example.yaml](config.example.yaml) shows all currently defined fields.
The public Go package provides `Parse([]byte)`, `Load(path)`, and
`LoadSelected(path, embeddedYAML)`. `LoadSelected` uses a nonempty explicit path
first and otherwise uses the supplied embedded bytes. It returns an error if
neither source exists. The parser accepts one YAML document, rejects unknown
fields, and validates it before any future boot operation can use it.

Validate a file with:

```sh
GOCACHE=/tmp go run ./cmd/goboot -config config.example.yaml -check
```

To embed a configuration, copy it to `cmd/goboot/embedded.yaml` before building:

```sh
cp my-config.yaml cmd/goboot/embedded.yaml
GOCACHE=/tmp go build -o goboot ./cmd/goboot
./goboot -check
```

The repository's `embedded.yaml` is empty by default. An explicit `-config`
path overrides embedded YAML. Without either, the executable reads
`/etc/goboot.yaml`. Without `-check`, it calls `Run`, waits for its runtime, and
applies `on_exit` when running as PID 1. The supported boot path rejects
modules, a different root, network settings, and hosts or resolver file contents
before changing system state. A boot error is logged and leaves PID 1 alive.
General service supervision and signal shutdown are still pending.

For development, `Run` requires a mount namespace separate from PID 1's, and
also a separate UTS namespace when setting a hostname. It makes mount
propagation private before mounting and refuses an ordinary host run before
mounting or setting the hostname. Set `logging.output: stderr` if the isolated
namespace has no `/dev/console`. The service receives a
deterministic environment: `HOME=/` and `PATH=/usr/bin:/bin`, then global
`environment` values, then service `environment` overrides. It runs directly
as an executable, without a shell. Its standard streams are inherited from
GoBoot. Boot lifecycle messages use `slog` on the configured output, opened
before mount changes; startup errors go to stderr, or `/dev/console` when PID 1.

The package's `SetupMounts(config)` applies the configured mounts through Linux
syscalls. It is intended for early boot or an isolated mount namespace and must
not be called on a normal host mount namespace. An empty mount list selects
`/proc`, `/dev`, `/sys`, and `/tmp` in that order. An existing mount with the
same source and filesystem type is reused when no flags or options were
requested. Conflicting mounts fail; `remount` applies to an existing target.
`Run` uses the same mount setup logic after the namespace check for development
runs, or directly when running as PID 1.

## QEMU boot test

Run `make qemu` to test the standalone executable and `make qemu-import` to
test a separate Go `/init` that imports the package, continues after `Run`
returns, and then waits for the configured service. Both targets build static
binaries and disposable initramfs images and check the probe's success marker.
It uses software emulation by default and needs no root permissions or KVM.
The default kernel is `/boot/vmlinuz-linux-lts`; override it with
`make qemu KERNEL=/path/to/vmlinuz` if needed. `QEMU_ACCEL=kvm` is optional on
hosts with usable KVM access. The guest config is
[`testdata/qemu/config.yaml`](testdata/qemu/config.yaml). This test exercises
the minimal PID 1 path; it does not cover module loading, root switching,
networking, or service restart.

| Field | Meaning and default |
| --- | --- |
| `version` | Required; must be `1`. |
| `logging.output` | Absolute file/device path, `stdout`, or `stderr`. Defaults to `/dev/console`. The future boot path will open the output before a root switch and retain its file descriptor through the switch. |
| `module_tree` | Clean absolute path containing kernel modules and dependency metadata. If omitted, the future loader will use `/lib/modules/<running-kernel-release>`. |
| `modules` | Module names or paths relative to `module_tree`. Order and duplicates do not matter; the future loader will resolve dependencies. Defaults to none. |
| `mounts` | Mount entries with `source`, absolute `target`, filesystem `type`, optional `flags`, and optional `options` string. If omitted or empty, the future boot path will mount `/proc`, `/dev`, `/sys`, and `/tmp` as described above. |
| `root` | Optional mount entry for a different root filesystem. Its target is a path in the initial root. |
| `hostname` | Optional hostname; at most 64 bytes. |
| `interfaces` | Optional static interface entries. Each has a unique `name`, CIDR `addresses`, and optional `routes` with CIDR `destination` and optional IP `gateway`. The future boot path will configure loopback by default. DHCP is not currently in scope. |
| `resolv_conf`, `hosts` | Optional string contents for `/etc/resolv.conf` and `/etc/hosts` on the final root. Omitted means untouched; `''` requests an empty file. `null` is invalid. |
| `environment` | Global process environment map. Keys must be shell-style variable names. |
| `services` | Entries with unique `name`, nonempty `command` array, optional absolute `directory`, optional `environment` map, and `restart` mode. The command executable must be an absolute path in the final root. `restart` defaults to `never`; allowed values are `never`, `on-failure`, `always`. |
| `on_exit` | Standalone executable action when services finish as PID 1: `wait` (default), `reboot`, or `poweroff`. An importing program chooses its own action. |

Mount flags currently accepted by validation are `readonly`, `nosuid`, `nodev`,
`noexec`, `sync`, `dirsync`, `remount`, `bind`, and `rbind`. All configured
filesystem paths must be clean absolute paths where indicated. Module names and
paths must not escape `module_tree`; redundant module entries are valid.

Configuration is validated now, but the precise handling of mount and module
paths across a root switch will be finalized with the privileged boot code.
