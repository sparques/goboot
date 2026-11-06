# goboot

GoBoot is a package and a standalone executable that replaces /sbin/init on a linux system.

It is configured through a single yaml file that may be embedded.
It is meant for embedded systems (like Raspberry Pis), thus some of the desktop boot tasks have been skipped (like keyboard setup and KMS).

GoBoot does not invoke external utilities for its own boot operations. Supply
any configured service binaries, kernel modules, and non-embedded configuration.

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
    - if a different root filesystem is specified, switch to it
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

The configuration parser, boot setup, and service supervisor are implemented.
Both the standalone executable and an importing Go `/init` boot in QEMU.
Additional QEMU fixtures cover root switching, static networking, service
restarts, orphan reaping, graceful shutdown, reboot, and poweroff.

## Go package API

An importing Go program can call `goboot.Run(ctx, config)`. It completes boot
setup, starts configured services in YAML order, and returns a
`*goboot.Runtime` without waiting for them. The caller can continue its own
`/init` work, use `runtime.Done()` or `runtime.Wait()` to observe completion,
or call `runtime.Stop()` to shut down managed services. `Run` does not apply
`on_exit`; that YAML field is used by the standalone executable. Zero services
are valid and yield an already completed runtime. A complete imported `/init`
example is in
[`testdata/qemu/init/main.go`](testdata/qemu/init/main.go).

The package also exposes the Linux operations it currently uses:

| Function | Operation |
| --- | --- |
| `Mount(MountConfig)` | Apply one mount with the Linux mount syscall. |
| `SetupMounts(*Config)` | Create targets, select defaults, and apply mounts. |
| `MakeMountsPrivate()` | Disable mount propagation to other namespaces. |
| `KernelModuleTree()`, `ResolveModules(tree, requests)` | Find the running kernel's module tree and resolve dependency order. |
| `LoadModule(path)`, `LoadModules(tree, requests)` | Insert a module or its full dependency set with `finit_module`. |
| `SwitchRoot(MountConfig)` | Mount and switch to a new root filesystem. |
| `PivotRoot(newRoot, putOld)`, `MoveMount(source, target)`, `Chroot(path)`, `Unmount(path, flags)` | Root-switch syscall operations. |
| `SetHostname(string)` | Set the UTS hostname. |
| `SetInterfaceUp(name)`, `AddAddress(name, cidr)`, `AddRoute(name, route)` | Configure links, addresses, and routes through rtnetlink. |
| `SetupNetwork(interfaces)` | Configure loopback and static interfaces. |
| `WriteSystemFiles(*Config)` | Atomically replace requested resolver and hosts files. |
| `SyncFilesystems()`, `Reboot(RebootAction)` | Flush writes and request restart or poweroff. |
| `ReapChildren()`, `WaitForChildren(ctx)` | Reap exited children after separately waited services have finished. |
| `Runtime.ReapOrphans(ctx)` | Optionally reap other PID 1 children while services run. Requires `/proc`. |

These functions are independently callable; `Run` uses the same mount and
hostname operations. The importing program owns its PID 1 lifetime and must
keep running after `Run` returns.

## Configuration (version 1)

[config.example.yaml](config.example.yaml) shows all currently defined fields.
For an initramfs that runs a single service and stays in that initramfs after
the service exits, a minimal configuration is:

```yaml
version: 1
services:
  - name: app
    command: [/bin/app]
```

To boot a separate ext4 root, include a `root` mount. The root device must
exist in the initramfs; GoBoot creates the target directory. `/bin/app` must
exist on the ext4 filesystem:

```yaml
version: 1
root: {source: /dev/vda, target: /newroot, type: ext4}
services:
  - name: app
    command: [/bin/app]
```

The QEMU root fixture is a runnable example in
[`testdata/qemu/root-config.yaml`](testdata/qemu/root-config.yaml).
Both examples use `slog` on `/dev/console`; set `logging.output` to an absolute
path, `stdout`, or `stderr` to change it.

The public Go package provides `Parse([]byte)`, `Load(path)`, and
`LoadSelected(path, embeddedYAML)`. `LoadSelected` uses a nonempty explicit path
first and otherwise uses the supplied embedded bytes. It returns an error if
neither source exists. The parser accepts one YAML document, rejects unknown
fields, and validates it before `Run` changes system state.

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

An explicit `-config` path overrides embedded YAML. Without either, the
executable reads `/etc/goboot.yaml`. Without `-check`, it calls `Run`, waits for
its runtime, and applies `on_exit` when running as PID 1. A boot error is
logged; PID 1 stays alive and reaps children.

For development, `Run` requires mount and network namespaces separate from PID
1's, and also a separate UTS namespace when setting a hostname. It makes mount
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

`make release` builds Linux x86-64, ARM64, and ARMv7 binaries under
`.build/release`. It sets `CGO_ENABLED=0` and checks the ELF program headers
for an interpreter or dynamic loader segment. The build host needs Go,
`readelf`, and `file`. To build for a single Linux architecture, use:

```sh
GOCACHE=/tmp CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o goboot-linux-arm64 ./cmd/goboot
file goboot-linux-arm64
readelf -l goboot-linux-arm64
```

The `file` output must say `statically linked`, and `readelf -l` must show no
`INTERP` or `DYNAMIC` program header. Install the binary as `/init` in the
initramfs, or build an importing Go `/init` that calls `goboot.Run`. Supply a
kernel and a matching module tree for the target board. Any configured service
binary, its script interpreter if applicable, and its runtime dependencies
must be present in the root where the service starts.

Run `make qemu` for the standalone boot path. `make qemu-import` verifies a
separate Go `/init` continues after `Run`; `make qemu-root` switches to an ext4
disk image; `make qemu-network` loads virtio modules and verifies static
networking and system files. `make qemu-restart`, `make qemu-orphan`, and
`make qemu-shutdown` exercise supervision; `make qemu-reboot` checks the reboot
policy. The root fixture checks a marker file that exists only on the ext4
image. These targets build static binaries and disposable initramfs images.
It uses software emulation by default and needs no root permissions or KVM.
The default kernel is `/boot/vmlinuz-linux-lts`; override it with
`make qemu KERNEL=/path/to/vmlinuz` if needed. `QEMU_ACCEL=kvm` is optional on
hosts with usable KVM access. The QEMU kernel command line passes an explicit
`-config /etc/goboot.yaml` to the standalone executable, so a local embedded
config does not change the fixture. The guest configs are in
[`testdata/qemu`](testdata/qemu). The network fixture copies compressed modules
from `MODULE_SOURCE`, which must match the QEMU kernel release.
`make verify` runs race-enabled Go tests, verifies static release binaries, and
runs all QEMU fixtures. QEMU tests need `qemu-system-x86_64`, `cpio`, an ext4
formatter, the configured kernel, and matching modules for the network fixture.
They run under software emulation without elevated privileges. If those tools,
the kernel, or matching modules are unavailable, run `GOCACHE=/tmp go test
-race ./...` and record which QEMU fixture was skipped and why. QEMU validates
the x86-64 boot path; ARM board firmware, device trees, storage, and real
network hardware still require testing on their target boards.
On 2026-11-06, `GOCACHE=/tmp make verify` passed: Go tests, the three static
release builds, and all eight QEMU fixtures. No physical board was tested.

| Field | Meaning and default |
| --- | --- |
| `version` | Required; must be `1`. |
| `logging.output` | Absolute file/device path, `stdout`, or `stderr`. Defaults to `/dev/console`. Opened before a root switch; its file descriptor stays open afterward. |
| `module_tree` | Clean absolute path containing kernel modules and `modules.dep`. If omitted, the loader uses `/lib/modules/<running-kernel-release>`. |
| `modules` | Module names or paths relative to `module_tree`. Order and duplicates do not matter; dependencies are loaded first. Defaults to none. |
| `mounts` | Mount entries with `source`, absolute `target`, filesystem `type`, optional `flags`, and optional `options` string. If omitted or empty, the boot path mounts `/proc`, `/dev`, `/sys`, and `/tmp` on the final root. |
| `root` | Optional mount entry for a different root filesystem. Its target is a path in the initial root and cannot be `/` or under `/dev`, `/proc`, or `/sys`. Omit it to retain the current root. |
| `hostname` | Optional hostname; at most 64 bytes. |
| `interfaces` | Optional static interface entries. Each has a unique `name` of at most 15 bytes, CIDR `addresses`, and optional `routes` with CIDR `destination` and optional same-family IP `gateway`. Loopback is always brought up with `127.0.0.1/8`. DHCP is not configured. |
| `resolv_conf`, `hosts` | Optional string contents for `/etc/resolv.conf` and `/etc/hosts` on the final root. Omitted means untouched; `''` requests an empty file. `null` is invalid. |
| `environment` | Global process environment map. Keys must be shell-style variable names. |
| `services` | Entries with unique `name`, nonempty `command` array, optional absolute `directory`, optional `environment` map, and `restart` mode. The command executable must be an absolute path in the final root. `restart` defaults to `never`; allowed values are `never`, `on-failure`, `always`. |
| `on_exit` | Standalone executable action when services finish as PID 1: `wait` (default), `reboot`, or `poweroff`. An importing program chooses its own action. |

Mount flags currently accepted by validation are `readonly`, `nosuid`, `nodev`,
`noexec`, `sync`, `dirsync`, `remount`, `bind`, and `rbind`. All configured
filesystem paths must be clean absolute paths where indicated. Module names and
paths must not escape `module_tree`; redundant module entries are valid.
`module_tree`, module paths, `root.source`, `root.target`, and `logging.output`
are resolved in the initial root. The log output is opened there before any
root switch, and its descriptor stays open. `mounts[].target`, service commands
and working directories, and `/etc/resolv.conf` and `/etc/hosts` are resolved
in the final root. `mounts[].source` is passed to the Linux mount syscall in
that final root; an absolute device or bind source must exist there. GoBoot
checks configured path syntax before changing system state. Filesystem
existence and mount availability are checked as each boot step runs.
The root target must be a real directory path in the initial root namespace;
GoBoot rejects a target that resolves through a symlink before mounting it.

Module names match the basename of an entry in `modules.dep`, without its `.ko`,
`.ko.gz`, `.ko.xz`, or `.ko.zst` suffix. Hyphens and underscores in names are
equivalent. A relative path must match an entry in `modules.dep` exactly.
Ambiguous names, missing entries, and dependency cycles stop boot before any
module is loaded. GoBoot uses `finit_module` and treats an already loaded module
as success. Loading compressed modules requires kernel support for
`MODULE_INIT_COMPRESSED_FILE`. The module tree is read from the initial root,
before mounts and a root switch. Importing programs can call
`ResolveModules`, `LoadModule`, or `LoadModules` independently of `Run`.

When `root` is set, GoBoot loads modules from the initial root, mounts the new
root, switches into it, then applies `mounts`, hostname, and services there.
It temporarily mounts `/proc` to inspect the mount table, and `/dev` when the
root source is a `/dev/` path; these temporary mounts are detached before the
switch. The final root receives `/proc`, `/dev`, `/sys`, and `/tmp` through the
normal mount setup unless `mounts` replaces those defaults. A regular mounted
root uses `pivot_root` and detaches the old root. Initramfs `rootfs` uses a
mount move and `chroot`, because [Linux cannot pivot `rootfs`](https://www.kernel.org/doc/html/v6.5/filesystems/ramfs-rootfs-initramfs.html);
its initial contents remain hidden in memory until reboot.

GoBoot configures networking after the final root is active: it brings `lo`
up, assigns `127.0.0.1/8`, then brings each configured interface up and adds
its addresses and routes in YAML order. Address additions accept an existing
address; a conflicting or duplicate route is an error. It does not run DHCP.
`resolv_conf` and `hosts` are written only when present in YAML. Each requested
file is written to a temporary file under `/etc` and atomically renamed over
the destination, including when the requested content is empty. Omitted fields
leave existing or missing files untouched. A network or file-write error stops
boot before starting the service; the standalone PID 1 logs the error and
stays alive.

Services start in YAML order and run concurrently. `restart: never` leaves a
service stopped after any exit; `on-failure` restarts after a nonzero exit or
signal; `always` restarts after every exit. Restart delays start at 250 ms,
double to a 30-second maximum, and reset after a run of at least 30 seconds.
An unrecoverable service failure stops the other managed services and makes
`Runtime.Wait` return an error. A normal exit leaves other services running;
when all services stop, `Runtime.Done` closes. On context cancellation or
`Runtime.Stop`, GoBoot sends SIGTERM, waits up to five seconds, then sends
SIGKILL to survivors. The standalone executable uses SIGINT and SIGTERM for
this shutdown path, then applies `on_exit` as PID 1. A non-PID-1 executable
exits without rebooting. With `on_exit: wait`, PID 1 stays alive and reaps
children indefinitely.

The standalone PID 1 reaps orphaned children while services run. It listens
for SIGCHLD and scans `/proc`, excluding managed service PIDs so service waits
retain their exit results. An importing `/init` can call
`Runtime.ReapOrphans(ctx)` if it wants GoBoot to reap its other children; this
is opt-in because those children may belong to the importing program. Keep
`/proc` mounted on the final root when using this reaper, including in an
explicit `mounts` list.
