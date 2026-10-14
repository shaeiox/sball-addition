# Production deployment runbook

This is the operational guide for putting `sball-addition` on a live CloudAvid
PVM cluster. It assumes you already read the [README](README.md) and know what the
hook system does; here we only care about *doing it safely on real nodes*.

Read the whole page once before the first rollout. Estimated time for a
three-node cluster: 20 minutes, of which 15 are waiting for quorate.

---

## 0. TL;DR

```console
# on the build host
make test && make bundle                 # -> release/*-bundle/ with SHA256SUMS

# on each node, in the order sball-rollout decides (workers first, master last)
scp release/sball-addition-1.0.0-bundle/sball-addition-*.rpm root@node:/root/
ssh root@node '
  rpm -Uvh /root/sball-addition-*.rpm &&
  sball-addition list &&
  sball-addition run-phase pre-quorate --dry-run &&
  sball-node-verify --quiet &&
  sball-addition watcher --once &&
  systemctl enable --now sball-addition-watcher.service
'
```

Or let `sball-rollout` do exactly that, one node at a time, waiting for quorate
in between:

```console
sudo sball-rollout --nodes=nodes.txt --rpm=/root/sball-addition-1.0.0-1.x86_64.rpm --smoke
```

**Nothing above restarts `sballd`.** This tool never starts, stops, restarts,
enables, disables or reconfigures the PVM service, and it installs no unit file,
drop-in or binary that belongs to it — so installing it is invisible to your
running VMs and to quorum. Starting the daemon through the wrapper (which is what
makes the hooks run at boot or at a restart) is a decision you make, once, with
`sball-integration apply`; the restart itself is always yours.

---

## 1. What you need

| Item | Notes |
|------|-------|
| Build host | any machine with Go ≥ 1.21, `make`, `python3` and `jq` (only for `make test`) |
| Target nodes | RHEL/CentOS 7+ (or equivalent), systemd, root access, `sballd` running |
| Cluster access | ssh to every node (the rollout script uses `ssh`/`scp` with `BatchMode`) |
| Change window | restarting `sballd` takes a node out of the cluster for the duration |

The released binary is **static** (`CGO_ENABLED=0`), so it does not care about
the glibc of the node and the same artifact runs on RHEL 7 and RHEL 9.

---

## 2. Build the artifact (build host, never on production)

```console
cd sball-addition
make test          # go vet + contract check + unit tests + man pages + 181 integration assertions
make bundle        # release/sball-addition-1.0.0-bundle/
```

`make bundle` produces:

```
release/sball-addition-1.0.0-bundle/
├── sball-addition-1.0.0.tar.gz              # self-contained: binary + installer + hooks + systemd
├── sball-addition-1.0.0-1.x86_64.rpm        # RPM (preferred on RHEL/CentOS)
├── sball-addition_1.0.0-1_amd64.deb         # DEB (Debian/Ubuntu; lintian clean)
├── README-DEPLOY.md                         # this runbook
├── DEPLOY-NOTES.txt                         # short, generated install/rollback sheet
└── SHA256SUMS                               # verify before shipping
```

Build individual artifacts when you only need one: `make tarball`, `make rpm`
(needs `rpm-build`), `make deb` (needs only `dpkg-deb`, so a RHEL build host can
produce it too). Both packages install the same tree, keep their configuration
files as `conffile`/`%config`, ship the man pages and the systemd units, and
never install, change or restart anything that belongs to the PVM service
(see `sball-integration` below for the one, hand-run exception).

```console
cd release/sball-addition-1.0.0-bundle && sha256sum -c SHA256SUMS
```

The tarball is self-contained: it carries a statically linked binary, so a node
without a Go toolchain (or without `make`) installs it as-is. `install.sh` uses
`--binary=FILE` when you pass it, otherwise the prebuilt `bin/sball-addition`
from the release, and only then falls back to building from source.

Sanity-check the artifact by installing it into a throw-away root (no root
privileges needed, nothing is touched outside the directory):

```console
mkdir -p /tmp/stage && ./scripts/install.sh --root=/tmp/stage --binary=bin/sball-addition
./scripts/verify-node.sh --root=/tmp/stage --smoke
```

Version/commit/build date are baked in with `-ldflags`; `make VERSION=1.0.1 ...`
rebuilds with a new version, and `sball-addition version` on a node tells you
exactly what is deployed there.

---

## 3. Rehearse with `--dry-run` (no changes at all)

Every command that touches hooks can be previewed:

```console
sball-addition list                                   # order, targets, enabled state
sball-addition run-phase pre-quorate --dry-run        # what a start would run
sball-addition run-phase post-quorate --dry-run --json
sball-addition validate                               # config, headers, dirs, socket
./scripts/install.sh --root=/tmp/rehearsal --dry-run  # what the installer would do
RPM=$(ls -1 release/sball-addition-*.x86_64.rpm | head -1)   # shell globs are not
sudo ./scripts/rollout.sh --nodes=nodes.txt --rpm="$RPM" --dry-run
```

`--dry-run` performs every skip decision (disabled, `RUN_ONCE`, `TARGET`,
not-executable) and reports the resolved order, timeouts, retry counts and
`ON_FAIL` policy — without running a single hook:

```console
$ sball-addition run-phase pre-quorate --dry-run
pre-quorate/storage-mount: would run (timeout 60s, up to 3 attempt(s), ON_FAIL=continue)
pre-quorate/resource-check: would run (timeout 30s, up to 1 attempt(s), ON_FAIL=abort)
```

---

## 4. Canary node first

Never start with the master, and never with a node you cannot afford to lose.

```console
scp release/sball-addition-1.0.0-bundle/sball-addition-1.0.0-1.x86_64.rpm root@node3:/root/

ssh root@node3
rpm -Uvh /root/sball-addition-1.0.0-1.x86_64.rpm     # installs files, does NOT touch sballd.service
systemctl daemon-reload                              # picks up our units only

sball-addition list                                  # hooks and their order
sball-addition run-phase pre-quorate --dry-run       # what a start would execute
sball-addition run-phase pre-quorate                 # optionally: run them now, by hand
sball-node-verify --smoke                            # deployment gate, incl. a real hook round trip

sball-addition watcher --once && pvm --stat          # is the node quorate? what does the table say?
systemctl enable --now sball-addition-watcher.service
sball-addition log --since=10m --pretty              # everything that ran so far
```

Then, and only then, when your maintenance window says so — restart the daemon
*during* which the hooks are attached. Apply the integration on the canary first:

```console
sball-integration status                             # expected: not applied (exit 1)
sball-integration apply                              # writes the ExecStart drop-in
# the command prints, it does not run:
systemctl daemon-reload && systemctl restart sballd.service
sball-addition watcher --once && sball-addition log --since=10m --pretty
```

If you would rather not attach the hooks to the start path at all, skip that
step: run the phases from your own runbook (`sball-addition run-phase
pre-quorate` before you start the daemon, `post-quorate` after it is up), or let
the watcher do the post-quorate part while the pre-quorate part stays manual.

Success criteria on the canary:

* `sball-addition list` shows the hooks you expect, none `INVALID`;
* the restart you performed ran the pre-quorate hooks **before** `sballd`
  started (log: `phase_start` → hook events → `phase_finish` → then the daemon
  pid appears) — with the integration applied;
* within 5 s of quorate the watcher logged `"event":"quorate"` and the
  post-quorate phase finished;
* `sball-node-verify` reports `NODE READY` (it reports "sballd.service left
  alone" until you apply the integration — that is the default, not a failure);
* VMs on the node keep running (the wrapper only runs hooks and `exec`s the
  daemon — it does not stop or start VMs).

Let it run for at least one full `sballd` restart cycle (including a reboot, if
your change window allows) before touching the next node.

---

## 5. Cluster rollout, quorum safe

```console
cat > nodes.txt <<'EOF'
# one host per line, comments and blank lines are ignored
node1.example.com
node2.example.com
node3.example.com
EOF

sudo sball-rollout --nodes=nodes.txt \
     --rpm=/root/sball-addition-1.0.0-1.x86_64.rpm \
     --maintenance="CHG-12345" --smoke --dry-run      # first: the plan
```

`sball-rollout` — installed as `/usr/bin/sball-rollout` — does per node:

1. check whether `sball-addition` is installed (prints the version),
2. copy and install the artifact (`rpm -Uvh`, or the tarball + its `install.sh`),
3. `sball-addition validate` — **aborts the node** if the hook set is broken,
4. poll `sball-addition watcher --once` until the node is quorate
   (default: up to `--wait-quorate=300` s; a node you did not restart answers
   immediately),
5. `sball-node-verify` (with `--smoke` if requested),

and only then continues with the next node. It **stops** on the first failure
unless you pass `--continue-on-error`, and it asks for a typed `yes` once before
it touches the first node.

It never restarts `sballd`, so it cannot cost you quorum: the restart (and the
moment for it) is yours. Because of that, "master last" is about **load**, not
about restarts — you still want the coordinator to keep running while the
workers are being updated, and if your own procedure includes a restart per node,
do it in the order the rollout prints.

Ordering: by default `sball-rollout` asks every node `sball-addition status`
whether it is the cluster master and moves master nodes to the end, so the
coordinator restarts last. Check the resolved order first:

```console
sudo sball-rollout --nodes=nodes.txt --list
```

Quorum rules of thumb:

* keep a quorate majority at all times — restarting one node at a time (which
  the script does) is safe as long as your cluster has at least 3 nodes or you
  know the failure domain of your cluster;
* if the cluster is already degraded (a node down, or `pvm --stat` showing
  `not-quorate`), **do not roll out** until it is healthy again;
* restarting the master is the riskiest step: it is last, and every node waits
  for quorate again before the next one is touched;
* if a node does not come back quorate, the script stops and prints the exact
  `ssh` command to investigate (status, `pvm --stat`, error log).

Tuning knobs: `--wait-quorate=N`, `--no-master-last` (keep the given order),
`--no-restart` (install + verify only, no service interruption),
`--no-verify`, `--ssh-user`, `--ssh-port`, `--ssh-opt`, `--continue-on-error`.

---

## 6. Hooks in production

The RPM/samples intentionally ship **safe** hooks: they log, they check, they
never abort. Promote them step by step:

**Step 1 – observe (week 1).** Leave `ON_FAIL: continue` everywhere. Read the log
after every restart and after every unexpected cluster event:

```console
sball-addition log --level=error --since=24h | jq -c '{ts,hook,event,exit,stderr}'
```

**Step 2 – make the important ones block.** Only checks that are *really* fatal
for this node should abort a start, and only after you have seen them pass for a
while:

```bash
### BEGIN SBALL-ADDITION INFO
# HOOK: pre-quorate
# NAME: storage-mount
# ON_FAIL: abort          # <-- promote only when the check is proven
# TIMEOUT: 60
### END SBALL-ADDITION INFO
```

A pre-quorate hook with `ON_FAIL: abort` that fails means the node stays **out**
of the cluster — safer than joining half configured, but it must be a decision
you make deliberately, and you must be able to fix it quickly:

```console
sball-addition disable storage-mount          # or
SBALL_ADDITION_SKIP_WRAPPER=1 systemctl start sballd.service   # emergency
```

Do not put network- or storage-dependent `abort` hooks on *all* nodes at once:
a cluster-wide dependency problem would then keep every node down. Prefer
`TARGET: master` for cluster-wide actions, and keep node-local abort hooks
limited to things that only exist on that node.

**Step 3 – extend.** Use the sample hooks as templates
(`docs/hook-template.sh`), keep the `AFTER` graph shallow, and give every hook a
realistic `TIMEOUT` — it is the only thing that protects the restart path from a
hanging script. Hooks run as root in their own process group; a timeout kills the
whole group, so no orphaned `ssh`/`curl` children survive.

Review checklist for a new hook: bash syntax (`bash -n`), header valid
(`sball-addition validate`), idempotent when run twice, `TARGET` correct,
`ON_FAIL` justified, `RUN_ONCE` only where "exactly once per boot" is truly the
requirement.

---

## 7. Monitoring and alerting

### 7.1 Prometheus (recommended)

`sball-addition metrics` renders the node and hook state in the Prometheus text
exposition format. A shipped oneshot unit plus a one minute timer feed a
node_exporter **textfile collector**, so nothing new has to be deployed on the
monitoring side:

```console
# on every node
install -d -m 0755 /var/lib/node_exporter/textfile_collector
echo 'SBALL_METRICS_ARGS=--out /var/lib/node_exporter/textfile_collector/sball-addition.prom' \
    >>/etc/sysconfig/sball-addition
systemctl enable --now sball-addition-metrics.timer
sball-addition metrics | head          # the same bytes, but on demand
```

The write is atomic (temp file + rename), so node_exporter never reads half a
file. If your node_exporter has no textfile collector (or you collect over ssh),
scrape the command instead:

```console
# scrape over ssh (e.g. a script_exporter or a cron + pushgateway setup)
ssh node 'sball-addition metrics'

# or let the timer keep running and just read the file over ssh
ssh node cat /var/lib/node_exporter/textfile_collector/sball-addition.prom
```

| Alert | Expression | For |
|-------|------------|-----|
| Node out of quorum | `sball_addition_quorate == 0` | 5m |
| PVM socket gone | `sball_addition_pvm_socket_reachable == 0` | 5m |
| sballd not running | `sball_addition_sballd_up == 0` | 5m |
| Watcher died | `sball_addition_watcher_running == 0` | 10m |
| Watcher gave up | `increase(sball_addition_poll_timeouts_total{window="1h"}[15m]) > 0` | 0m |
| A start was refused | `increase(sball_addition_phase_aborts_total{window="1h"}[15m]) > 0` | 0m |
| Hook keeps failing | `increase(sball_addition_hook_failures_total[1h]) > 2` | 0m |
| Hooks stopped running | `time() - sball_addition_hook_last_success_timestamp_seconds > 86400` | 15m |
| Hook got slow | `sball_addition_hook_last_duration_seconds > 60` | 10m |
| Hooks silently skipped | `increase(sball_addition_hook_skips_total[24h]) > 0` | 0m |

`window="1h|24h|7d"` counters are computed from the JSON log tail, so they keep
their history across a restart of the timer or the node. If a node is
intentionally down, use `metrics --no-socket`: the socket, `quorate` and
`is_master` series are then **omitted** (not reported as 0) so your alerts
follow the maintenance window instead of firing.

`is_master` is only exported when the master node id is known (the node has seen
a `sballMaster()` value); a node that cannot tell who the master is produces no
sample rather than a wrong one.

Grafana: graph `sball_addition_hooks{state="enabled"}` per phase to see hook
drift, and `sball_addition_phase_duration_seconds{phase="pre-quorate"}` to catch
a restart path that is getting slower.

### 7.2 jq recipes (no monitoring stack)

The watcher and the runner never write to stdout; everything lands in
`/var/log/sball-addition/sball-addition.log` as JSON lines. Alert on:

| What | Rule |
|------|------|
| A node never becomes quorate | `{"event":"poll_timeout"}` in the last 15 min |
| A phase had to abort | `{"event":"phase_abort"}` (a start was refused) |
| A hook keeps failing | `{"event":"give_up"}` or two `fail` events for the same `hook` within 1 h |
| Hooks became slow | `{"event":"phase_finish","phase":"pre-quorate"}` with `duration_ms` above a node-specific budget |
| Watcher restarts in a loop | `systemctl show -p NRestarts sball-addition-watcher.service` climbing |

```console
# cheap health check for your monitoring agent (exit 0 = healthy)
sball-addition log --since=15m --level=error | jq -e 'select(.event=="poll_timeout" or .event=="phase_abort")' >/dev/null && exit 1 || exit 0

# how often did the watcher give up on this node?
sball-addition log --since=24h | jq -r 'select(.event=="poll_timeout") | .ts' | wc -l
```

Also worth monitoring: `sball-addition-metrics.timer` itself
(`systemctl is-active sball-addition-metrics.timer`), `systemctl is-active sball-addition-watcher.service`,
`sball -addition status --json` as a whole-node check (the JSON is stable and
easy to feed into any agent), and hook-specific markers that your own hooks
create.

Log volume is small (a few dozen lines per restart plus one line per hook run);
`logrotate` keeps 8 weeks by default and the watcher reopens the file on
`SIGHUP`, so rotation never loses or duplicates a record. If a hook prints a lot,
only the last 64 KiB per stream are kept and trimmed to a single `stderr` field.

---

## 8. Day-2 operations

```console
# change one hook
sudo vi /etc/sball-addition/hooks.d/post-quorate.d/30-register-monitoring.sh
sball-addition validate && sball-addition list
sball-addition run register-monitoring --force --quiet    # test it without a restart

# retry the whole phase without a restart
sball-addition run-phase post-quorate --force

# silence a hook that is noisy but not worth fixing today
sball-addition disable register-monitoring

# upgrade the package (hooks and configuration are preserved: %config(noreplace))
rpm -Uvh sball-addition-1.0.1-1.x86_64.rpm
systemctl daemon-reload && systemctl restart sball-addition-watcher.service
sball-addition version          # confirm the new build

# rotate the log manually
logrotate -f /etc/logrotate.d/sball-addition
```

Upgrades are rolling like installs: one node, `sball-node-verify --smoke`,
restart the watcher (the watcher is *our* unit — restarting it does not touch
`sballd` and the node stays in the cluster). An upgrade never restarts the
daemon, so a package update is free of quorum risk; if your hooks changed and you
want them applied to a running node, use `sball-addition run-phase ... --force`
instead of restarting `sballd`.

Keep `/etc/sball-addition` in git (hooks are code): commit hooks, deploy with
your configuration management, and let `sball-addition validate` be the gate in
CI. Because hooks live in the config tree, `rpm -Uvh` never overwrites them.

**Ansible** users: `deploy/ansible/sball-addition.yml` is a one-play template
that installs the RPM, runs the node verifier and restarts the service — extend
it with your hook files (`role`-style `copy` into
`/etc/sball-addition/hooks.d/<phase>.d/`).

---

## 9. Rollback and emergency

| Situation | Action |
|-----------|--------|
| A hook blocks the start and you need the node up **now** | `SBALL_ADDITION_SKIP_WRAPPER=1 systemctl start sballd.service` |
| The watcher misbehaves | `systemctl stop sball-addition-watcher.service` (hooks stop firing, `sballd` is unaffected) |
| The wrapper is suspected | `sball-integration remove` (drops the drop-in / restores the daemon), then reload and restart `sballd` yourself whenever it suits the cluster |
| Roll the package back | `rpm -Uvh --oldpackage sball-addition-<previous>.rpm && systemctl daemon-reload` |
| Remove it entirely | `scripts/uninstall.sh` (stops our watcher, removes the drop-in or restores the daemon *if* they were applied, deletes everything, proves nothing is left) |
| Remove the RPM, keep hooks/state | `rpm -e sball-addition` |

`sball-addition` is additive by design: it installs a wrapper, its own units and
a hook tree, and — only if you applied it — a drop-in or a shim for the service.
Removing those returns the node to a plain `/usr/sbin/sballd` start: no database,
no state you must migrate, nothing that survives inside the daemon. Between
"installed" and "hooks attached to the start path" every operation in this
runbook is reversible without ever signalling `sballd`, let alone stopping it.

---

## 10. Security notes

* Hooks execute **as root**. Treat `/etc/sball-addition` like `/etc/sudoers.d`:
  `chown -R root:root /etc/sball-addition && chmod 0700 /etc/sball-addition`
  (`sball-node-verify` fails the node if a hook is group- or world-writable).
* Prefer absolute paths and `sball_require` inside hooks; a hook inherits the
  daemon's environment (including `PATH` from the systemd unit, which is
  `PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin` by default).
* Secrets: hooks get their configuration from `/etc/sysconfig/sball-addition`
  (mode 0600 if it holds tokens) or from the environment; never hardcode tokens
  into a hook that is committed to git.
* SELinux: on enforcing systems the wrapper runs from `/usr/bin` and hooks from
  `/etc` — both are within the normal `bin_t`/`etc_t` domains, and the wrapper
  only executes the daemon and the CLI. If you write hooks that talk to a
  daemon over its socket, check `ausearch -m avc -ts recent` when something
  fails oddly, and label your hook files with `semanage fcontext` rather than
  turning SELinux off.
* The watcher only needs the local PVM socket; `sball-addition-watcher.service`
  ships with `NoNewPrivileges=yes` and no network capabilities of its own.
* Review the JSON log for anything your hooks print: stdout/stderr of failing
  hooks is stored (up to 2 KB per event) — do not print credentials.

---

## 11. Go-live checklist

Copy this into your change ticket:

```
[ ] artifact built with `make test && make bundle`, SHA256SUMS verified
[ ] staging node ran a full sballd restart with the new version
[ ] `sball-addition list` reviewed: order, targets, ON_FAIL, RUN_ONCE, TIMEOUT
[ ] `sball-addition validate` clean on every node (or problems documented)
[ ] `sball-node-verify --smoke` = NODE READY on every node before its restart
[ ] cluster healthy before the window: `pvm --stat` shows all nodes quorate
[ ] rollout plan reviewed: `sball-rollout --nodes=nodes.txt --list` (master last)
[ ] change window announced; VM owners informed (nodes leave the cluster briefly)
[ ] monitoring rules for poll_timeout / phase_abort / give_up in place
[ ] rollback rehearsed on the canary (`SBALL_ADDITION_SKIP_WRAPPER=1` + `sball-integration remove`)
[ ] after the rollout: watcher enabled on every node, errors from the last hour read
[ ] hooks are committed to git with the commit hash in the ticket
```

---

## 12. Troubleshooting quick reference

| Symptom | First command | Likely cause |
|---------|---------------|--------------|
| hooks do not run at start | `sball-integration status` | nothing is wired in (the default): apply it, then reload + restart the daemon yourself |
| hooks do run when you test them but not at boot | `grep ExecStart /usr/lib/systemd/system/sballd.service /etc/systemd/system/sballd.service.d/*.conf` | the drop-in is not applied, or `daemon-reload` was not run after applying it |
| node does not start after the rollout | `sball-addition log --level=error --since=10m` | pre-quorate hook with `ON_FAIL: abort`; `SBALL_ADDITION_SKIP_WRAPPER=1` gets you up |
| post-quorate hooks never fire | `systemctl status sball-addition-watcher`, `sball-addition status` | watcher not enabled/running, or the node is not quorate |
| `watcher` exits 5 | `cat /var/lib/sball-addition/runtime/watcher.lock` | another watcher already runs (only one per node) |
| `watcher` exits 6 | `sball-addition log \| jq 'select(.event=="poll_timeout")'` | node never became quorate in `poll_timeout_seconds` |
| a hook is skipped | `sball-addition log \| jq 'select(.event=="skip")'` | disabled / not executable / `RUN_ONCE` / `TARGET=master` on a worker |
| `INVALID` in `list` | `sball-addition validate` | malformed header; the line number is in the error |
| no log records | `ls -l /var/log/sball-addition` | log path not writable; the CLI warns and falls back to stderr |

More detail on every message: README → *Troubleshooting*, and
`sball-addition log --pretty`.
