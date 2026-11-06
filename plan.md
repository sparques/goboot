# GoBoot implementation plan

GoBoot is a Linux-only Go package and executable that can run as PID 1. Its own
boot operations use Go and Linux syscalls; the only programs it executes are
those explicitly listed in the configuration. A deployment can contain just a
statically linked GoBoot binary and, if services are configured, their required
executables and interpreters.

## 1. Define the configuration and package boundary

- [x] Add a versioned example YAML file and document every field, default, and
  validation rule in the README. Cover modules (names or paths, with a documented
  module search tree and dependency metadata), mounts (source, target,
  filesystem type, flags, options), optional new root, hostname, network
  interfaces and addresses/routes, DNS and hosts contents, environment,
  services, restart policy, logging output, and exit/reboot policy. Default
  logging output to `/dev/console` when the YAML field is absent.
- [x] Provide a public package API such as `Load`, `Validate`, and `Run(ctx,
  config)`. `Run` must return after setup and service startup, with a runtime
  handle for waiting; an importing Go `/init` retains control of its process.
  Keep configuration parsing separate from privileged operations so it can be
  tested without root. Use an explicit interface for Linux operations only
  where it makes meaningful unit tests possible.
- [x] Export Go-idiomatic functions for the Linux operations already used by
  GoBoot: `Mount`, `MakeMountsPrivate`, `SetHostname`, `SyncFilesystems`,
  `Reboot`, and child reaping. Keep these callable independently of `Run`.
- [x] Support a YAML file path for development and deployment. Add an optional
  build-time embedded YAML path; define precedence if both are present. Make a
  missing or invalid configuration fatal before changing system state. Document
  how to build a binary with embedded configuration.
- [x] Decide and document how paths are resolved before and after a root switch.
  Validate mount targets, module paths, service commands, and file destinations;
  reject contradictory or unsafe combinations early.

**Done when:** config parsing and validation tests cover defaults (including
`/dev/console` logging), invalid values, a configured log destination, file
versus embedded selection, and a complete example config.

## 2. Establish a minimal PID 1 boot path

- [x] Add `cmd/goboot` with `log/slog` for boot and service lifecycle logs, and a
  non-PID-1 mode for development. Keep any PID-1-specific behavior explicit.
  Open `/dev/console` as the default log destination before boot operations;
  allow the YAML config to select a different output destination. Define when
  configured paths are opened relative to a root switch, and ensure config or
  log-output errors can still be reported to `/dev/console`.
- [x] Implement mount setup using Linux syscalls. When no mounts are configured,
  ensure `/proc` (`proc`), `/dev` (`devtmpfs`, if absent), `/sys` (`sysfs`), and
  `/tmp` (`tmpfs`) are mounted. Define whether an explicit mount list replaces
  these defaults, as the README specifies. Create mount points as needed; handle
  already mounted filesystems deliberately.
- [x] Set the hostname from config, if present. Set up the configured process
  environment, including a deterministic baseline and per-service overrides.
- [x] Start one configured foreground service and wait for it, proving the
  executable can boot a minimal system without invoking shell commands for its
  own setup.

**Done when:** a disposable initramfs or QEMU image boots GoBoot as PID 1,
mounts the default filesystems, and runs a configured service. No host mounts or
hostname are changed by automated tests.

## 3. Resolve kernel modules and set up the root filesystem

- [x] Resolve each configured module and its transitive dependencies from the
  module tree's dependency metadata (for example, `modules.dep`), without
  invoking `modprobe` or another external binary. Document how names and paths
  map to that tree, and report missing or ambiguous dependencies clearly. Expose
  the module-loading syscall through an exported package function.
- [x] Deduplicate modules by their resolved identity, including modules named
  more than once or listed both directly and as dependencies. Treat redundant
  config entries as valid. Detect dependency cycles, then load each required
  module once in dependency-first order with the Linux module-loading syscall.
  The order of entries in YAML must not affect correctness. Handle modules
  already loaded by the kernel and report the module and syscall error on other
  failures.
- [x] Implement an optional root switch only when the configured root differs
  from the current root. Mount the target root; use `pivot_root` and detach the
  old root when possible, or move and `chroot` from initramfs `rootfs` (which
  cannot be pivoted). Define the treatment of `/dev`, `/proc`, and `/sys` across
  the switch so the final root has the configured filesystems. Export the root
  switch operation for callers that need to compose their own init sequence.
- [x] Specify failure behavior for each privileged boot step: emit an actionable
  `slog` error and enter a documented safe PID-1 failure state or reboot policy.

**Done when:** unit tests cover transitive dependencies, reversed config order,
duplicate entries, already-loaded modules, missing dependencies, and cycles; a
QEMU test boots from an initramfs, switches into a separate root, and verifies
mounts, hostname, and service execution from the final root.

## 4. Configure networking and system files

- [x] Bring up `lo` and assign `127.0.0.1/8` by default. Implement optional
  interface state, addresses, and routes through Linux netlink/syscalls, with no
  dependency on `ip`, `ifconfig`, or DHCP executables. Define whether DHCP is
  in scope; initially accept only explicit static settings unless implemented.
  Export the networking operations for importing Go programs.
- [x] Write `/etc/resolv.conf` and `/etc/hosts` on the final root only when their
  respective YAML fields are present. An absent field leaves an existing or
  missing file untouched. A present field replaces the file, including when
  configured with empty content. Use safe file creation and replacement.
- [x] Give interface and file-write failures clear context; document which
  failures stop boot and which, if any, can be configured as nonfatal.

**Done when:** a network namespace or QEMU test verifies default loopback,
static interface settings, and the absent/present semantics of both files.

## 5. Supervise services and handle PID 1 lifecycle

- [x] Define service command/arguments, working directory, environment,
  startup order, and restart modes (`never`, `on-failure`, `always`). State what
  counts as failure, and add bounded restart delay/backoff to avoid a busy loop.
- [x] Reap all children, including orphans adopted by PID 1. Coordinate SIGCHLD,
  service exits, and configured restart decisions without losing exit events.
- [x] Handle termination signals: stop services, wait for a bounded grace
  period, force-stop survivors, and reap children. Define what happens if a
  service exits or all configured services stop.
- [x] Implement configured reboot/poweroff behavior via the Linux reboot syscall
  after clean shutdown. Define the non-PID-1 behavior separately so development
  runs do not reboot the host.

**Done when:** integration tests cover normal exits, crashes, restart limits or
backoff, orphan reaping, signal shutdown, and reboot-policy selection.

## 6. Build, validate, and document deployment

- [x] Add build instructions for a statically linked Linux binary and the target
  architectures used by embedded boards. Verify static linking for the release
  build rather than assuming it from build flags.
- [x] Add unit tests for config and pure decision logic, and isolated QEMU boot
  tests for mounts, networking, PID 1, root switch, and shutdown. Use host
  namespace tests only where available; record an explicit reason if a required
  QEMU fixture cannot run.
- [x] Document a minimal config, a root-switch config, config embedding, the
  default `/dev/console` logging and YAML output setting, boot failure recovery,
  and the need to supply any configured service binaries or script interpreters.
- [x] Run `goimports` on Go files and use `GOCACHE=/tmp` for Go commands in this
  workspace. Check the finished implementation against every README task.

**Done when:** the documented examples boot in QEMU, test results and any
hardware-only validation limits are recorded, and the README matches the
implemented configuration and behavior.

Validation on 2026-11-06: `GOCACHE=/tmp make verify` passed Go race tests,
static ELF checks for Linux x86-64, ARM64, and ARMv7, and all eight unprivileged
QEMU fixtures. Physical embedded boards and their storage, network, firmware,
and device trees have not been tested.
