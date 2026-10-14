# sball-addition

A managed, standalone hook system for the **`sballd`** service (CloudAvid PVM
virtualization cluster). It runs administrator supplied scripts at two points of
the service lifecycle:

| Phase | When | Typical use |
|-------|------|-------------|
| **pre-quorate** | before `sballd` starts, while the service is still down | mount storage, check prerequisites, fence/cleanup leftovers, abort a start that cannot succeed |
| **post-quorate** | after the node joined the cluster and reports `quorate` | register with monitoring, sync configuration, warm caches, notify, reload agents |

Highlights:

* **Wrapper based** – `sballd-wrapper` runs the pre-quorate hooks and then
  `exec`s the real daemon, so the pid, the signal handling and the service unit
  behaviour of `sballd` stay exactly as they were.
* **Quorate watcher** – a small process polls the PVM XML-RPC socket
  (`127.0.0.1:7090`) until the local node reports `sball-state = quorate` and
  then runs the post-quorate hooks. Default reaction time: ~2 seconds.
* **Declarative hooks** – every hook describes itself in a header
  (`TIMEOUT`, `RETRY`, `ON_FAIL`, `RUN_ONCE`, `AFTER`, `TARGET`, `ENABLED`).
* **JSON logging** – one line per event, `jq` friendly, optionally rotated with
  `logrotate` (the watcher reopens on `SIGHUP`).
* **Self contained** – a single static Go binary, plain bash hooks, no external
  service, no database, no Python, nothing to configure before it works.

---

## Table of contents

1. [Quick start](#quick-start)
2. [How it works](#how-it-works)
3. [Installation](#installation)
4. [Configuration](#configuration)
5. [Writing hooks](#writing-hooks)
6. [CLI reference](#cli-reference)
7. [Logging](#logging)
8. [Operations](#operations)
9. [Directory layout](#directory-layout)
10. [Building, testing, packaging](#building-testing-packaging)
11. [Troubleshooting](#troubleshooting)
12. [Acceptance criteria](#acceptance-criteria)
13. [Limitations and roadmap](#limitations-and-roadmap)

---

## Quick start

```console
# one command installation (binary, wrapper, config, samples, systemd units)
sudo ./scripts/install.sh --with-systemd

# look at what was installed
sball-addition list

# wire sballd.service to the wrapper and restart the service
sudo systemctl daemon-reload
sudo systemctl restart sballd.service

# keep the post-quorate watcher alive across daemon restarts
sudo systemctl enable --now sball-addition-watcher.service

# watch it work
sball-addition status
sball-addition log --follow --pretty
```

Without an installation you can try everything inside a throw-away root:

```console
make build
./bin/sball-addition --root=/tmp/sball-demo list
./bin/sball-addition --root=/tmp/sball-demo watcher --once   # "is the node quorate yet?"
```

---

## How it works

```
                       systemd (sballd.service)
                                 |
                                 v
                     /usr/bin/sballd-wrapper          <-- ExecStart (drop-in)
                                 |
              +------------------+-------------------+
              |                                      |
   1. sball-addition run-phase pre-quorate    2. sball-addition watcher
      (timeout / retry / ON_FAIL)                (polling, file locked)
              |                                      |
      OK -----+                                      |  poll socket every 2s
              |                                quorate?  --> run-phase post-quorate
              v                                      v
        exec /usr/sbin/sballd.real            post-quorate.d/ hooks
              (the real daemon, same pid)
```

* **Pre-quorate** runs in the foreground of the service start. A hook with
  `ON_FAIL: abort` that keeps failing after its retries makes the wrapper exit
  non-zero, so systemd reports a failed start and `sballd` is never executed –
  the node stays out of the cluster instead of joining half configured.
* **Post-quorate** runs in the watcher, which is *not* part of the daemon
  process. That keeps `sballd` untouched and lets the hooks run even when the
  daemon takes minutes to join the cluster.
* The watcher takes an `flock` on
  `/var/lib/sball-addition/runtime/watcher.lock`, so the copy spawned by the
  wrapper, the systemd unit and manual runs can never execute hooks twice.
  The lock is released by the kernel even after `kill -9`.

### Fail open

A hook system must never brick a virtualization host, so the wrapper separates
"the hooks ran and failed" from "the hook system itself is broken":

| `run-phase` exit | Wrapper behaviour |
|------------------|-------------------|
| `0` | hooks succeeded → `exec sballd` |
| `1` (hook failed with `ON_FAIL: abort`) | **abort** the start (the node must not join half configured) |
| `6` (pre-quorate phase timed out) | **abort** the start |
| missing binary | warn and start `sballd` anyway |
| `2`/`3`/`4` (usage, configuration, invalid hook set) | warn, log an error and start `sballd` anyway |

`SBALL_ADDITION_REQUIRED=1` (env or `/etc/sysconfig/sball-addition`) makes every
error fatal; `SBALL_ADDITION_SKIP_WRAPPER=1` bypasses all hooks in an emergency.

---

## Installation

### One command (recommended)

```console
sudo ./scripts/install.sh [options]

  --with-systemd      install the watcher unit and the sballd.service drop-in
  --enable-service    also enable + start the watcher unit
  --shim-sballd       also wrap /usr/sbin/sballd itself (see below)
  --with-samples      force the sample hooks (default: only when a phase dir is empty)
  --no-samples        never install sample hooks
  --force             overwrite the existing configuration/samples
  --root=DIR          install into DIR (tests, containers, chroots)
```

The script is idempotent, keeps an existing configuration (saving the old one as
`*.rpmsave`) and finishes by running `sball-addition validate`.

### RPM

```console
make tarball          # release/sball-addition-1.0.0.tar.gz (binary + all files)
make rpm              # needs rpm-build; produces release/sball-addition-*.rpm
```

The RPM installs the same tree, ships the systemd unit and the drop-in, and
**does not** modify `sballd.service` behind your back – the drop-in is a file
you can delete (and `rpm -e` does exactly that).

### Choosing how `sballd` is started

PVM starts its daemon in two different ways, and you probably want to cover both.

**A. systemd (default)** – point the unit at the wrapper:

```ini
# /etc/systemd/system/sballd.service.d/sball-addition.conf  (installed for you)
[Service]
ExecStart=
ExecStart=/usr/bin/sballd-wrapper
```

```console
sudo systemctl daemon-reload && sudo systemctl restart sballd.service
```

**B. shim (covers `pvm-wrapper --start` and manual starts)** – `pvm --start`
executes `/usr/sbin/sballd -r` directly, which bypasses systemd and therefore the
wrapper. The shim moves the real binary aside and puts the wrapper in its place:

```console
sudo ./scripts/install.sh --shim-sballd
# /usr/sbin/sballd      -> wrapper
# /usr/sbin/sballd.real -> the original daemon (auto-detected by the wrapper)
```

Use A, B or both; the lock and the phase state are shared, so nothing runs twice.

### Manual layout

| Path | Content |
|------|---------|
| `/usr/bin/sball-addition` | the CLI/binary |
| `/usr/bin/sballd-wrapper`, `/usr/sbin/sballd-wrapper` | the service wrapper |
| `/usr/lib/systemd/system/sball-addition-watcher.service` | watcher unit |
| `/etc/systemd/system/sballd.service.d/sball-addition.conf` | ExecStart override |
| `/etc/sball-addition/…` | configuration, hooks, helper library |
| `/var/lib/sball-addition/…` | state (RUN_ONCE) and runtime files |
| `/var/log/sball-addition/sball-addition.log` | JSON log |
| `/etc/logrotate.d/sball-addition` | log rotation (HUP based) |

---

## Configuration

`/etc/sball-addition/sball-addition.conf` – every value is optional, the file
below is exactly what the built-in defaults are:

```ini
[general]
log_level = info                                 # debug | info | warn | error
log_file = /var/log/sball-addition/sball-addition.log
socket_host = 127.0.0.1
socket_port = 7090
default_timeout = 60                             # seconds, when a hook has no TIMEOUT

[pre-quorate]
enabled = true
on_failure = abort                               # abort | continue
continue_on_empty = true                         # start sballd when no hook exists

[post-quorate]
enabled = true
poll_interval_seconds = 2                        # quorate polling cadence
poll_timeout_seconds = 300                       # give up after this long
on_failure = continue                            # abort | continue
```

An optional `[paths]` section (`etc_dir`, `hooks_dir`, `state_dir`,
`runtime_dir`, `log_dir`) exists for tests and unusual layouts.

Precedence: **built-in defaults → config file → environment → CLI flags**.

Environment overrides: `SBALL_ADDITION_ROOT`, `SBALL_ADDITION_LOG_LEVEL`,
`SBALL_ADDITION_LOG_FILE`, `SBALL_ADDITION_SOCKET_HOST`,
`SBALL_ADDITION_SOCKET_PORT`.

`on_failure` is the phase default; a hook overrides it with its own
`ON_FAIL` field.

---

## Writing hooks

A hook is an executable bash script with a self-describing header:

```bash
#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: register-monitoring
# DESCRIPTION: Register node in monitoring system
# TIMEOUT: 60
# RETRY: 2
# RETRY_BACKOFF: 5
# ON_FAIL: continue
# RUN_ONCE: false
# AFTER: network-check
# TARGET: local
# ENABLED: true
### END SBALL-ADDITION INFO

set -euo pipefail
source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"

sball_info "registering ${SBALL_HOSTNAME} (master_id=${SBALL_MASTER_ID})"
```

| Field | Required | Values | Default | Meaning |
|-------|----------|--------|---------|---------|
| `HOOK` | yes | `pre-quorate` / `post-quorate` | – | phase the hook belongs to |
| `NAME` | yes | unique string | – | identifier used by `run`, `enable`, `AFTER`, … |
| `DESCRIPTION` | no | text | – | shown by `list` and `status` |
| `TIMEOUT` | no | seconds | `general.default_timeout` | SIGTERM, then SIGKILL after 5 s |
| `RETRY` | no | integer | `0` | additional attempts |
| `RETRY_BACKOFF` | no | seconds | `5` | delay between attempts |
| `ON_FAIL` | no | `abort` / `continue` | phase policy | stop the phase or log and go on |
| `RUN_ONCE` | no | `true` / `false` | `false` | once per server lifetime (state file) |
| `AFTER` | no | hook **name** | – | ordering dependency inside the phase |
| `TARGET` | no | `local` / `master` / `all` | `local` | on which nodes to run |
| `ENABLED` | no | `true` / `false` | `true` | enable/disable without deleting |

Header values may use `${VAR}` and `${VAR:-default}` placeholders which are
expanded from the environment when the hook is loaded.

### Execution order

1. numeric filename prefix (`10-…` before `20-…`), then filename – for
   zero-padded names this is the plain alphabetical order of the PRD;
2. then the `AFTER` dependency (a stable topological sort, deterministic across
   nodes);
3. unknown `AFTER` targets and circular dependencies are reported as errors by
   `list`, `validate`, `run-phase` and the watcher instead of being executed in
   an arbitrary order.

### Environment injected into every hook

| Variable | Example | Notes |
|----------|---------|-------|
| `SBALL_HOOK_PHASE` | `post-quorate` | |
| `SBALL_HOOK_NAME` | `register-monitoring` | |
| `SBALL_HOSTNAME` | `spongebob` | short hostname |
| `SBALL_MASTER_ID` | `1` | from `sballMaster()`; `0`/empty when unknown |
| `SBALL_IS_MASTER` | `true` / `false` | |
| `SBALL_QUORATE_STATE` | `quorate` | `down`/`unknown` before quorate |
| `SBALL_LOG_FILE` | `/var/log/sball-addition/sball-addition.log` | |
| `SBALL_HOOK_PATH`, `SBALL_HOOK_DIR` | | absolute paths |
| `SBALL_STATE_DIR`, `SBALL_STATE_FILE` | | RUN_ONCE marker location |
| `SBALL_RUNTIME_DIR` | `/var/lib/sball-addition/runtime` | |
| `SBALL_COMMON_LIB` | `/etc/sball-addition/lib/common.sh` | helper library |
| `SBALL_RUN_ID`, `SBALL_RETRY_ATTEMPT`, `SBALL_RETRY_MAX` | | correlation |
| `SBALL_ADDITION_VERSION` | `1.0.0` | |

### Helper library

`/etc/sball-addition/lib/common.sh` (sourced through `$SBALL_COMMON_LIB`) gives
hooks JSON logging into the same file and a few idempotency helpers:

```bash
sball_info "message"        sball_warn "message"       sball_error "message"
sball_debug "message"       # only with SBALL_HOOK_DEBUG=1
sball_have curl             sball_require curl jq
sball_run "reload agent" systemctl reload my-agent
sball_retry 3 5 curl -sf https://…/health
sball_is_master             # [[ "${SBALL_IS_MASTER}" == true ]]
sball_mounted /var/lib/pvm-storage
sball_wait_for 30 ssh -o BatchMode=yes master true
sball_mark_state            sball_state_file
```

### Sample hooks

`hooks/` ships six working examples (installed by `install.sh` when the phase
directories are still empty, and by the RPM):

| Hook | Phase | Demonstrates |
|------|-------|--------------|
| `10-storage-mount.sh` | pre | idempotent mount, `sball_require`, `sball_run` |
| `20-resource-check.sh` | pre | required paths/tools/free space, `ON_FAIL: abort` |
| `10-network-check.sh` | post | master reachability, `sball_retry` |
| `20-sync-config.sh` | post | `TARGET: master`, `RUN_ONCE: true`, `AFTER` |
| `30-register-monitoring.sh` | post | HTTP registration with retries |
| `90-notify.sh` | post | `TARGET: all`, `AFTER`, own JSON log records |

They read their settings from the environment (`/etc/sysconfig/sball-addition`),
for example `SBALL_MONITORING_URL`, `SBALL_STORAGE_DEVICE`,
`SBALL_REQUIRED_PATHS`. Without configuration they only log – safe to leave
enabled.

---

## CLI reference

```
sball-addition [global options] <command> [command options]
```

| Command | Purpose |
|---------|---------|
| `list [--phase=…] [--json] [--strict]` | hooks in execution order with metadata |
| `run <name> [--phase=…] [--force] [--no-state] [--quiet]` | run one hook now |
| `run-phase <phase> [--force] [--quiet] [--json]` | run a whole phase (used by the wrapper) |
| `status [--json]` | sballd, watcher, cluster and last hook outcomes |
| `log [--follow] [--hook=] [--phase=] [--since=] [--level=] [--limit=] [--pretty]` | JSON log queries |
| `enable` / `disable <name> [--phase=…]` | flip `ENABLED` inside the hook header |
| `install-hook <file> --phase=… [--order=…] [--force]` | validate + install a hook |
| `uninstall-hook <name> [--phase=…] [--keep-state]` | remove a hook and its state |
| `validate [--phase=…] [--json]` | configuration, paths, headers, ordering, socket |
| `watcher [--foreground] [--once] [--poll-interval=] [--poll-timeout=]` | wait for quorate, run post hooks |
| `version [--json]`, `help` | |

Global options: `--root`, `--config`, `--log-level`, `--log-file`, `--socket`,
`--no-log`.

Exit codes: `0` ok · `1` hook failed/phase aborted · `2` usage · `3`
configuration · `4` not found / invalid hooks · `5` watcher lock held · `6`
quorate poll timeout. The wrapper propagates them, so monitoring can alert on a
failed start.

```console
$ sball-addition list
PHASE         ORDER  NAME                 ENABLED  TARGET  DESCRIPTION
pre-quorate   10     storage-mount        yes      local   Mount storage before start
pre-quorate   20     resource-check       yes      local   Verify node prerequisites
post-quorate  10     network-check        yes      local   Verify cluster connectivity
post-quorate  20     sync-config          yes      master  Sync the shared configuration
post-quorate  30     register-monitoring  yes      local   Register node in monitoring
post-quorate  90     notify               yes      all     Broadcast the quorate event

$ sball-addition status
sball-addition status
  Version:        1.0.0
  Host:           spongebob
  Config:         /etc/sball-addition/sball-addition.conf
  Log file:       /var/log/sball-addition/sball-addition.log
  Socket:         127.0.0.1:7090 (reachable, 3 node(s))
  sballd PID:     12345
  sballd Uptime:  00:36:12
  sballd State:   UP (quorate)
  Cluster Master: node 1 (spongebob) [this node]
  Watcher:        running (pid 12399, uptime 00:36:10)
  Hooks:          pre-quorate 2/2 enabled (on), post-quorate 4/4 enabled (on)
  Last pre-hook:  10-storage-mount (OK, 2s ago)
  Last post-hook: 20-sync-config (OK, 10m ago)
```

---

## Logging

Every event is one JSON object (JSON lines) written to
`/var/log/sball-addition/sball-addition.log`:

```json
{"ts":"2026-10-14T12:00:00Z","level":"info","pid":1234,"phase":"pre-quorate","hook":"10-storage-mount","event":"start","host":"spongebob","target":"local","is_master":true,"attempt":1,"timeout":60,"run_id":"20261014T120000Z-1234"}
{"ts":"2026-10-14T12:00:02Z","level":"info","pid":1234,"phase":"pre-quorate","hook":"10-storage-mount","event":"finish","exit":0,"duration_ms":2000,"attempt":1,"run_id":"20261014T120000Z-1234"}
{"ts":"2026-10-14T12:05:01Z","level":"error","pid":1299,"phase":"post-quorate","hook":"20-sync-config","event":"fail","exit":1,"duration_ms":812,"attempt":1,"stderr":"connection refused"}
```

Levels: `debug`, `info`, `warn`, `error`. Events: `phase_start`,
`phase_finish`, `phase_abort`, `phase_empty`, `phase_disabled`, `start`,
`finish`, `fail`, `timeout`, `retry`, `give_up`, `skip`, `target_unknown`,
`hook_error`, `hook_warning`, `quorate`, `poll`, `poll_timeout`,
`watcher_start`, `watcher_stop`, `hook_log` (from `lib/common.sh`),
`hook_enabled`, `hook_disabled`, `hook_installed`, `hook_uninstalled`,
`manual_run`, `log_reopened`, …

`run_id` correlates every record of one phase execution.

```console
# failures of the last hour
sball-addition log --since=1h | jq -c 'select(.event=="fail")'

# how long the post-quorate hooks took
sball-addition log | jq -r 'select(.event=="phase_finish" and .phase=="post-quorate") | .duration_ms'

# what the watcher did
sball-addition log --hook="" --pretty | grep watcher

# alerts
sball-addition log --level=error --since=15m | jq -r 'select(.event=="poll_timeout") | "PVM node \(.host) never became quorate"'
```

Rotation: `/etc/logrotate.d/sball-addition` rotates weekly and sends `SIGHUP` to
the watcher, which reopens the file; no hooks are interrupted.

---

## Operations

**Is everything healthy?**

```console
sball-addition status && sball-addition validate
```

**Turn a noisy hook off without deleting it**

```console
sball-addition disable register-monitoring     # rewrites ENABLED: false in the header
sball-addition enable  register-monitoring
```

**Re-run a hook that already ran** (`RUN_ONCE: true`)

```console
sball-addition run sync-config --force
```

**Wait for quorate from your own automation**

```console
sball-addition watcher --once || echo "node is not quorate yet"
```

**Emergency bypass**

```console
SBALL_ADDITION_SKIP_WRAPPER=1 systemctl start sballd.service
```

**Uninstall**

```console
sudo ./scripts/uninstall.sh                 # removes everything, then proves it
sudo ./scripts/uninstall.sh --keep-config   # keep configuration and hooks
sudo rpm -e sball-addition                  # remove the RPM (keeps state/logs)
```

`uninstall.sh` stops/disables the watcher unit, removes the `ExecStart`
override, restores `/usr/sbin/sballd` if the shim was used, then deletes the
programs, hooks, configuration, state and logs and reports anything that
survived (it should be nothing).

---

## Directory layout

```
/etc/sball-addition/
├── sball-addition.conf
├── hooks.d/
│   ├── pre-quorate.d/
│   └── post-quorate.d/
└── lib/
    └── common.sh

/var/lib/sball-addition/
├── state/
│   ├── pre-quorate/<hook>.state       # RUN_ONCE markers
│   └── post-quorate/<hook>.state
└── runtime/
    ├── watcher.pid
    ├── watcher.lock                   # flock, one watcher per node
    └── last-run.json                  # last outcome per hook (used by status)

/var/log/sball-addition/
└── sball-addition.log
```

---

## Building, testing, packaging

```console
make build          # bin/sball-addition (static, no runtime deps)
make test           # go vet + unit tests + full integration suite
make unit           # go test ./...
make integration    # sandboxed end-to-end run (fake PVM socket, needs python3 + jq)
make install        # install.sh with default options
make tarball        # release/sball-addition-<version>.tar.gz
make rpm            # RPM built from that tarball
make clean
```

The integration suite (`test/integration/run.sh`) installs into a temporary root
and drives a **fake PVM socket** (`test/integration/fake-pvm.py`) that speaks the
real framed XML-RPC protocol, so no PVM installation is needed. It covers: the
one command install, `list`, `run`, pre-quorate before `exec`, `ON_FAIL: abort`
blocking the start, post-quorate hooks firing < 5 s after quorate, poll timeout,
`RUN_ONCE`, `TARGET=master`, retry with backoff, timeout/SIGTERM handling,
`status`, `jq`-valid logs, `validate`, the sample hooks, the watcher lock and a
complete uninstall (102 assertions).

---

## Troubleshooting

| Symptom | Likely cause / fix |
|---------|--------------------|
| `hooks` never run and `status` says `sballd PID: 0` | the wrapper is not used: check `systemctl cat sballd.service` for the `ExecStart` override, or install the shim (`--shim-sballd`). |
| `Watcher: not running` | `systemctl status sball-addition-watcher`; run `sball-addition watcher --foreground` to see the reason. Exit 5 means another watcher holds the lock. |
| Hooks do not run although the node is quorate | the socket query must see *this* node: compare `sballd-wrapper` hostname with `pvm --stat`; the runner matches the short hostname (and falls back to interface IPs). |
| `sballd` does not start any more | a pre-quorate hook failed with `ON_FAIL: abort` – read `sball-addition log --level=error`, or start with `SBALL_ADDITION_SKIP_WRAPPER=1`. |
| `ERROR ... poll_timeout` | the node did not reach quorate within `poll_timeout_seconds` (default 300 s). Raise it for slow clusters; systemd restarts the watcher automatically. |
| A hook is skipped | `sball-addition log` shows the reason: `disabled`, `not-executable` (`chmod +x`), `run-once-already-done` (`--force`), `target-master-not-master`. |
| `list` prints `INVALID` or problems | the header is malformed – `sball-addition validate` shows file and line. |
| Log file not written | the path in the config must be writable by root; the CLI warns and logs to stderr when it cannot open it. |
| Timeouts do not kill children | impossible by design: hooks run in their own process group and get SIGTERM, then SIGKILL after 5 s. |

---

## Acceptance criteria

| Criterion | Where it is implemented / verified |
|-----------|-------------------------------------|
| Installation with one command | `scripts/install.sh` (and `make rpm`); integration section 1 |
| `list` shows all hooks with metadata | `internal/cli/cmd_list.go`; integration section 2 |
| `run <name>` manually runs a hook | `internal/cli/cmd_run.go`; integration section 4 |
| pre-quorate hooks run before `exec` | `scripts/sballd-wrapper`; integration section 5 (sequence file proof) |
| a broken install never blocks the service | `scripts/sballd-wrapper` fail-open policy; integration section 6b |
| post-quorate hooks within 5 s of quorate | `internal/watcher`; integration section 7 (measured) |
| timeout/failure follows `ON_FAIL` | `internal/runner`; integration sections 6, 12 |
| logs are `jq` parseable | `internal/log`; integration section 14 |
| `RUN_ONCE` skips the second run | `internal/runner/state.go`; integration section 9 |
| `TARGET=master` only on the master | `internal/runner.RunOne`; integration section 10 |
| retry with backoff | `internal/runner`; integration section 11 |
| `status` shows the full state | `internal/cli/cmd_status.go`; integration section 13 |
| watcher logs an error and exits on poll timeout | `internal/watcher`; integration section 8 (exit 6) |
| uninstall leaves nothing | `scripts/uninstall.sh`; integration section 18 |

Unit tests additionally cover the header parser, ordering (including cycles and
unknown dependencies), the XML-RPC client, the runner (timeouts, retries,
RUN_ONCE, targets, environment), the JSON logger, the file lock and the flag
parser.

---

## Limitations and roadmap

Current MVP limitations (as agreed in the PRD):

* `TARGET=all` does not coordinate across nodes – every node runs its own copy
  of the hook. Use it for idempotent, node-local actions only.
* There is no rollback: a failing hook logs, and (with `ON_FAIL: abort`) stops
  the phase. Nothing is reverted.
* Only systemd is supported for the watcher lifecycle.
* Hooks are not digitally signed.

Planned phases: retry/backoff hardening → distribution (RPM/DEB, man page) →
SaaS style management (HTTP API, central hook repository, signed/versioned
hooks, real cluster-wide coordination).

## License

MIT – see the CloudAvid distribution guidelines for the PVM product line.
