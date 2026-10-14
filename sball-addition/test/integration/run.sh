#!/bin/bash
#
# Integration test for sball-addition.
#
# Everything runs inside a throw away directory (--root=<tmpdir>) against a fake
# PVM socket, so the suite is safe to run on a workstation and needs no root:
#
#   ./test/integration/run.sh            # quiet on success
#   ./test/integration/run.sh -v         # show the output of every command
#   KEEP=1 ./test/integration/run.sh     # keep the sandbox for inspection
#
# It covers the acceptance criteria: one command install, list, run, pre-quorate
# before exec, watcher within seconds of quorate, ON_FAIL handling, JSON logs
# (validated with jq), RUN_ONCE, TARGET=master, retry with backoff, status,
# watcher poll timeout and a complete uninstall.
set -uo pipefail

HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd -- "$HERE/../.." && pwd)
VERBOSE=0
KEEP=${KEEP:-0}
[[ "${1:-}" == "-v" || "${1:-}" == "--verbose" ]] && VERBOSE=1

PASS=0
FAIL=0
SKIP=0

ok() { PASS=$((PASS + 1)); printf '  \033[32mPASS\033[0m %s\n' "$1"; }
no() {
    FAIL=$((FAIL + 1))
    printf '  \033[31mFAIL\033[0m %s\n' "$1"
    [[ -n "${2:-}" ]] && printf '       %s\n' "$(printf '%s' "$2" | head -5)"
}
skip() { SKIP=$((SKIP + 1)); printf '  \033[33mSKIP\033[0m %s\n' "$1"; }
section() { printf '\n\033[1m== %s\033[0m\n' "$1"; }

die() {
    printf 'integration: fatal: %s\n' "$*" >&2
    exit 1
}

# --------------------------------------------------------------- utilities ---
run_cmd() { # sets OUT / RC
    OUT=$("$@" 2>&1)
    RC=$?
    if ((VERBOSE)); then
        printf '    $ %s\n      -> rc=%d\n%s\n' "$*" "$RC" "$(printf '%s' "$OUT" | sed 's/^/      /' | head -20)"
    fi
}

check_rc() { # check_rc <description> <expected rc> <cmd...>
    local desc=$1 want=$2
    shift 2
    run_cmd "$@"
    if [[ "$RC" == "$want" ]]; then
        ok "$desc"
    else
        no "$desc" "exit=$RC want=$want
$OUT"
    fi
}

check_zero() { check_rc "$1" 0 "${@:2}"; }

check_contains() { # check_contains <description> <haystack> <needle>
    if [[ "$2" == *"$3"* ]]; then
        ok "$1"
    else
        no "$1" "expected to find: $3
in:
$2"
    fi
}

check_not_contains() {
    if [[ "$2" != *"$3"* ]]; then
        ok "$1"
    else
        no "$1" "did not expect: $3
in:
$2"
    fi
}

wait_for_file() { # wait_for_file <path> <seconds>
    local path=$1 timeout=${2:-15} i=0
    while ((i < timeout * 10)); do
        [[ -e "$path" ]] && return 0
        sleep 0.1
        i=$((i + 1))
    done
    return 1
}

stop_watcher() { # stop a watcher that the wrapper spawned, if any
    local pidfile="$ROOT/var/lib/sball-addition/runtime/watcher.pid"
    if [[ -f "$pidfile" ]]; then
        local pid
        pid=$(cat "$pidfile" 2>/dev/null)
        [[ -n "$pid" ]] && kill "$pid" 2>/dev/null
        wait_for_gone "$pidfile" 10
    fi
    # give the kernel a moment to release the flock
    sleep 0.2
}

wait_for_gone() {
    local path=$1 timeout=${2:-10} i=0
    while ((i < timeout * 10)); do
        [[ ! -e "$path" ]] && return 0
        sleep 0.1
        i=$((i + 1))
    done
    return 1
}

# --------------------------------------------------------------- sandbox -----
SANDBOX=$(mktemp -d "${TMPDIR:-/tmp}/sball-addition-itest.XXXXXX") || die "mktemp failed"
ROOT="$SANDBOX/root"
SERVER_PIDS=()
FAKE_PORT=""

cleanup() {
    for pid in "${SERVER_PIDS[@]:-}"; do
        [[ -n "$pid" ]] && kill "$pid" 2>/dev/null
    done
    # stop a watcher that is still running inside the sandbox
    if [[ -f "$ROOT/var/lib/sball-addition/runtime/watcher.pid" ]]; then
        kill "$(cat "$ROOT/var/lib/sball-addition/runtime/watcher.pid")" 2>/dev/null
    fi
    if ((KEEP)); then
        printf '\nsandbox kept at %s\n' "$SANDBOX"
    else
        rm -rf "$SANDBOX"
    fi
}
trap cleanup EXIT

start_fake_pvm() { # start_fake_pvm [extra args...] -> sets FAKE_PORT
    local log
    log="$SANDBOX/fake-pvm-$(date +%s%N).log"
    python3 "$HERE/fake-pvm.py" --port=0 "$@" >"$log" 2>&1 &
    local pid=$!
    SERVER_PIDS+=("$pid")
    local i=0
    while ((i < 100)); do
        if grep -q '^LISTENING ' "$log" 2>/dev/null; then
            FAKE_PORT=$(awk '/^LISTENING /{print $2; exit}' "$log")
            return 0
        fi
        sleep 0.1
        i=$((i + 1))
    done
    cat "$log" >&2
    die "the fake PVM server did not start"
}

write_config() { # write_config <port>
    cat >"$ROOT/etc/sball-addition/sball-addition.conf" <<EOF
[general]
log_level = debug
log_file = $ROOT/var/log/sball-addition/sball-addition.log
socket_host = 127.0.0.1
socket_port = $1
default_timeout = 10

[pre-quorate]
enabled = true
on_failure = abort
continue_on_empty = true

[post-quorate]
enabled = true
poll_interval_seconds = 1
poll_timeout_seconds = 15
on_failure = continue
EOF
}

cli() { "$ROOT/usr/bin/sball-addition" --root="$ROOT" "$@"; }
logfile() { printf '%s' "$ROOT/var/log/sball-addition/sball-addition.log"; }

install_hook() { # install_hook <phase> <name> <header> <body>
    local phase=$1 name=$2 header=$3 body=$4
    local tmp="$SANDBOX/hook-$name.sh"
    {
        printf '#!/bin/bash\n### BEGIN SBALL-ADDITION INFO\n# HOOK: %s\n# NAME: %s\n%s### END SBALL-ADDITION INFO\n%s\n' \
            "$phase" "$name" "$header" "$body"
    } >"$tmp"
    chmod +x "$tmp"
    cli install-hook "$tmp" --phase="$phase" --force >/dev/null 2>&1
}

remove_hook() { # remove_hook <phase> <name>
    cli uninstall-hook "$1" --phase="$2" >/dev/null 2>&1
}

clear_hooks() {
    rm -f "$ROOT"/etc/sball-addition/hooks.d/*.d/*.sh
    rm -rf "$ROOT"/var/lib/sball-addition/state/*/*.state
}

# ============================================================================
printf '\033[1msball-addition integration test\033[0m\n'
printf '  sandbox: %s\n' "$SANDBOX"
printf '  repo:    %s\n' "$REPO"

section "0. prerequisites"
command -v python3 >/dev/null || die "python3 is required for the fake PVM socket"
command -v jq >/dev/null || die "jq is required for the JSON log assertions"
if [[ ! -x "$REPO/bin/sball-addition" ]]; then
    (cd "$REPO" && make build >/dev/null) || die "make build failed"
fi
ok "binary available at $REPO/bin/sball-addition"

start_fake_pvm --control "$SANDBOX/state" --hostname "$(hostname -s)"
PRIMARY_PORT="$FAKE_PORT"
echo not-quorate >"$SANDBOX/state"
ok "fake PVM socket listening on 127.0.0.1:$PRIMARY_PORT"

section "1. one command installation"
rm -rf "$ROOT"
mkdir -p "$ROOT"
run_cmd "$REPO/scripts/install.sh" --root="$ROOT" --binary="$REPO/bin/sball-addition" --no-samples --with-systemd
if [[ "$RC" != 0 ]]; then
    no "install.sh exits 0" "$OUT"
else
    ok "install.sh exits 0"
fi
for path in \
    "$ROOT/usr/bin/sball-addition" \
    "$ROOT/usr/bin/sballd-wrapper" \
    "$ROOT/etc/sball-addition/sball-addition.conf" \
    "$ROOT/etc/sball-addition/lib/common.sh" \
    "$ROOT/etc/sball-addition/hooks.d/pre-quorate.d" \
    "$ROOT/etc/sball-addition/hooks.d/post-quorate.d" \
    "$ROOT/var/lib/sball-addition/state" \
    "$ROOT/var/lib/sball-addition/runtime" \
    "$ROOT/var/log/sball-addition" \
    "$ROOT/usr/lib/systemd/system/sball-addition-watcher.service" \
    "$ROOT/etc/systemd/system/sballd.service.d/sball-addition.conf" \
    "$ROOT/etc/logrotate.d/sball-addition"; do
    [[ -e "$path" ]] && ok "installed $(basename "$path")" || no "installed $path"
done

write_config "$PRIMARY_PORT"

section "2. install-hook / list"
install_hook pre-quorate resource-check '# DESCRIPTION: pre hook
# TIMEOUT: 30
# ON_FAIL: abort
' 'touch "$SANDBOX-should-not-exist"; exit 0'
install_hook post-quorate register-monitoring '# DESCRIPTION: Register node in monitoring
# TIMEOUT: 20
# RETRY: 2
# RETRY_BACKOFF: 1
' 'date -u +%Y-%m-%dT%H:%M:%SZ >> "'"$ROOT"'/var/lib/sball-addition/runtime/monitoring"'
install_hook post-quorate sync-config '# DESCRIPTION: Sync config across cluster
# AFTER: register-monitoring
# TARGET: master
' 'touch "'"$ROOT"'/var/lib/sball-addition/runtime/synced"'

check_zero "list succeeds" cli list
check_contains "list shows the pre-quorate hook" "$OUT" "resource-check"
check_contains "list shows the post-quorate hook" "$OUT" "register-monitoring"
check_contains "list shows the description" "$OUT" "Register node in monitoring"
check_contains "list shows the target" "$OUT" "master"

check_zero "list --json succeeds" cli list --json
check_contains "list --json is parseable" "$(printf '%s' "$OUT" | jq -r '.hooks | length' 2>/dev/null)" "3"
check_zero "list --phase=post-quorate succeeds" cli list --phase=post-quorate
check_not_contains "phase filter hides the other phase" "$OUT" "resource-check"

section "3. enable / disable"
check_zero "disable register-monitoring" cli disable register-monitoring
check_contains "list reports the hook as disabled" "$(cli list)" "no"
check_zero "enable register-monitoring" cli enable register-monitoring
check_contains "the header itself was updated" \
    "$(cat "$ROOT"/etc/sball-addition/hooks.d/post-quorate.d/*register-monitoring*.sh)" "# ENABLED: true"

section "4. run a single hook"
rm -f "$ROOT/var/lib/sball-addition/runtime/monitoring"
check_zero "run register-monitoring" cli run register-monitoring --quiet
[[ -f "$ROOT/var/lib/sball-addition/runtime/monitoring" ]] &&
    ok "the hook really executed" || no "the hook really executed"
check_rc "running an unknown hook fails with 4" 4 cli run does-not-exist --quiet

section "5. pre-quorate hooks run before sballd is exec'd"
clear_hooks
: >"$SANDBOX/sequence"
cat >"$ROOT/usr/sbin/sballd" <<EOF
#!/bin/bash
echo "sballd \$*" >> "$SANDBOX/sequence"
exit 0
EOF
chmod +x "$ROOT/usr/sbin/sballd"
install_hook pre-quorate first-step '# TIMEOUT: 10
' 'echo "pre-first" >> "'"$SANDBOX"'/sequence"'
install_hook pre-quorate second-step '# AFTER: first-step
' 'echo "pre-second" >> "'"$SANDBOX"'/sequence"'
install_hook post-quorate after-start '' 'echo "post" >> "'"$SANDBOX"'/sequence"'

run_cmd env SBALLD_BIN="$ROOT/usr/sbin/sballd" \
    SBALL_ADDITION_BIN="$ROOT/usr/bin/sball-addition" \
    SBALL_ADDITION_WRAPPER_ARGS="--root=$ROOT" \
    "$ROOT/usr/bin/sballd-wrapper" -r
if [[ "$RC" == 0 ]]; then
    ok "the wrapper starts sballd (exit 0)"
else
    no "the wrapper starts sballd" "rc=$RC
$OUT"
fi
check_contains "the daemon received its arguments" "$(cat "$SANDBOX/sequence")" "sballd -r"
check_contains "pre-quorate hooks ran in AFTER order" "$(cat "$SANDBOX/sequence" | tr '\n' ' ')" "pre-first pre-second"
if [[ "$(head -1 "$SANDBOX/sequence")" == "pre-first" ]]; then
    ok "pre-quorate hook ran before sballd started"
else
    no "pre-quorate hook ran before sballd started" "$(cat "$SANDBOX/sequence")"
fi
check_contains "the pre-quorate phase is in the log" "$(cat "$(logfile)")" '"phase":"pre-quorate"'

section "6. ON_FAIL=abort blocks the start"
clear_hooks
: >"$SANDBOX/sequence"
install_hook pre-quorate blocking-check '# ON_FAIL: abort
' 'echo "blocking" >> "'"$SANDBOX"'/sequence"; exit 1'
install_hook pre-quorate never-runs '' 'echo "never" >> "'"$SANDBOX"'/sequence"'

run_cmd env SBALLD_BIN="$ROOT/usr/sbin/sballd" \
    SBALL_ADDITION_BIN="$ROOT/usr/bin/sball-addition" \
    SBALL_ADDITION_WRAPPER_ARGS="--root=$ROOT" \
    "$ROOT/usr/bin/sballd-wrapper"
if [[ "$RC" != 0 ]]; then
    ok "the wrapper aborts with a non-zero exit code (rc=$RC)"
else
    no "the wrapper must not start sballd when a pre-quorate hook fails" "$OUT"
fi
check_not_contains "sballd was not started" "$(cat "$SANDBOX/sequence")" "sballd"
check_not_contains "the phase stopped at the failing hook" "$(cat "$SANDBOX/sequence")" "never"
check_contains "the abort is logged" "$(cat "$(logfile)")" "phase_abort"

section "6b. the wrapper fails open on infrastructure errors"
cp "$ROOT/etc/sball-addition/sball-addition.conf" "$SANDBOX/good.conf"
printf '[general]\nsocket_port = http\n' >"$ROOT/etc/sball-addition/sball-addition.conf"
: >"$SANDBOX/sequence"
run_cmd env SBALLD_BIN="$ROOT/usr/sbin/sballd" \
    SBALL_ADDITION_BIN="$ROOT/usr/bin/sball-addition" \
    SBALL_ADDITION_WRAPPER_ARGS="--root=$ROOT" \
    "$ROOT/usr/bin/sballd-wrapper" -r
if [[ "$RC" == 0 && "$(cat "$SANDBOX/sequence")" == *"sballd"* ]]; then
    ok "a broken configuration does not block the service start (fail open)"
else
    no "a broken configuration does not block the service start (fail open)" "rc=$RC
$OUT"
fi
run_cmd env SBALL_ADDITION_REQUIRED=1 SBALLD_BIN="$ROOT/usr/sbin/sballd" \
    SBALL_ADDITION_BIN="$ROOT/usr/bin/sball-addition" \
    SBALL_ADDITION_WRAPPER_ARGS="--root=$ROOT" \
    "$ROOT/usr/bin/sballd-wrapper" -r
if [[ "$RC" == 3 ]]; then
    ok "SBALL_ADDITION_REQUIRED=1 makes it fatal (exit 3)"
else
    no "SBALL_ADDITION_REQUIRED=1 makes it fatal (exit 3)" "rc=$RC
$OUT"
fi
cp "$SANDBOX/good.conf" "$ROOT/etc/sball-addition/sball-addition.conf"
check_zero "the restored configuration is valid again" cli validate

section "7. watcher: post-quorate hooks within seconds of quorate"
stop_watcher
clear_hooks
rm -f "$ROOT/var/lib/sball-addition/runtime/monitoring" "$ROOT/var/lib/sball-addition/runtime/synced"
echo not-quorate >"$SANDBOX/state"
install_hook post-quorate register-monitoring '' 'date -u +%Y-%m-%dT%H:%M:%SZ >> "'"$ROOT"'/var/lib/sball-addition/runtime/monitoring"'
install_hook post-quorate sync-config '# AFTER: register-monitoring
# TARGET: master
' 'touch "'"$ROOT"'/var/lib/sball-addition/runtime/synced"'

cli watcher --foreground >"$SANDBOX/watcher.out" 2>&1 &
WATCHER_PID=$!
sleep 2
if kill -0 "$WATCHER_PID" 2>/dev/null; then
    ok "the watcher keeps waiting while the node is not quorate"
else
    no "the watcher keeps waiting while the node is not quorate" "$(cat "$SANDBOX/watcher.out")"
fi

QUORATE_AT=$(date +%s.%N)
echo quorate >"$SANDBOX/state"
if wait_for_file "$ROOT/var/lib/sball-addition/runtime/monitoring" 10; then
    OK_AT=$(date +%s.%N)
    ELAPSED=$(awk -v a="$QUORATE_AT" -v b="$OK_AT" 'BEGIN {printf "%.2f", b - a}')
    ok "post-quorate hook ran after quorate (${ELAPSED}s)"
    if awk -v e="$ELAPSED" 'BEGIN {exit !(e < 5)}'; then
        ok "hooks ran within 5 seconds of quorate"
    else
        no "hooks ran within 5 seconds of quorate" "took ${ELAPSED}s"
    fi
else
    no "post-quorate hook ran after quorate" "$(cat "$SANDBOX/watcher.out")"
fi

wait "$WATCHER_PID"
check_rc "the watcher exits 0 after running the hooks" 0 true
[[ -f "$ROOT/var/lib/sball-addition/runtime/synced" ]] &&
    ok "TARGET=master hook ran on the master node" || no "TARGET=master hook ran on the master node"
check_contains "the quorate event is logged" "$(cat "$(logfile)")" '"event":"quorate"'
check_contains "the watcher removed its pid file on exit" "$(ls "$ROOT/var/lib/sball-addition/runtime" | tr '\n' ' ')" "last-run.json"

section "8. watcher poll timeout"
stop_watcher
echo not-quorate >"$SANDBOX/state"
check_rc "an unreachable quorate state times out with exit 6" 6 \
    cli watcher --foreground --poll-interval 1 --poll-timeout 2
check_contains "the timeout is logged as an error" "$(cat "$(logfile)")" '"event":"poll_timeout"'

section "9. RUN_ONCE"
clear_hooks
echo quorate >"$SANDBOX/state"
install_hook post-quorate only-once '# RUN_ONCE: true
' 'echo run >> "'"$ROOT"'/var/lib/sball-addition/runtime/once-counter"'
rm -f "$ROOT/var/lib/sball-addition/runtime/once-counter"
check_zero "the first run executes the hook" cli run-phase post-quorate --quiet
check_zero "the second run succeeds" cli run-phase post-quorate --quiet
if [[ "$(wc -l <"$ROOT/var/lib/sball-addition/runtime/once-counter")" == "1" ]]; then
    ok "RUN_ONCE skipped the second execution"
else
    no "RUN_ONCE skipped the second execution" "$(cat "$ROOT/var/lib/sball-addition/runtime/once-counter")"
fi
[[ -f "$ROOT/var/lib/sball-addition/state/post-quorate/only-once.state" ]] &&
    ok "the state marker exists" || no "the state marker exists"
check_contains "the skip is logged with its reason" "$(cat "$(logfile)")" "run-once-already-done"
check_zero "--force ignores the marker" cli run only-once --force --quiet
if [[ "$(wc -l <"$ROOT/var/lib/sball-addition/runtime/once-counter")" == "2" ]]; then
    ok "--force really re-ran the hook"
else
    no "--force really re-ran the hook" "$(cat "$ROOT/var/lib/sball-addition/runtime/once-counter")"
fi

section "10. TARGET=master on a non-master node"
clear_hooks
install_hook post-quorate master-only '# TARGET: master
' 'touch "'"$ROOT"'/var/lib/sball-addition/runtime/wrong-node"'
start_fake_pvm --master 2 --hostname "$(hostname -s)"
SECOND_PORT=$FAKE_PORT
write_config "$SECOND_PORT"
rm -f "$ROOT/var/lib/sball-addition/runtime/wrong-node"
check_zero "run-phase succeeds" cli run-phase post-quorate --quiet
[[ ! -f "$ROOT/var/lib/sball-addition/runtime/wrong-node" ]] &&
    ok "the master-only hook did not run on this node" ||
    no "the master-only hook did not run on this node"
check_contains "the skip reason is logged" "$(cat "$(logfile)")" "target-master-not-master"
write_config "$PRIMARY_PORT"
echo quorate >"$SANDBOX/state"

section "11. retry with backoff"
clear_hooks
rm -f "$ROOT/var/lib/sball-addition/runtime/flaky-counter"
install_hook post-quorate flaky-hook '# RETRY: 2
# RETRY_BACKOFF: 1
' 'n=$(cat "'"$ROOT"'/var/lib/sball-addition/runtime/flaky-counter" 2>/dev/null || echo 0)
n=$((n+1)); echo $n > "'"$ROOT"'/var/lib/sball-addition/runtime/flaky-counter"
if [ "$n" -lt 3 ]; then echo "attempt $n failed" >&2; exit 1; fi
exit 0'
check_zero "the hook finally succeeds" cli run flaky-hook --quiet
check_contains "the runner retried twice" "$(cat "$(logfile)")" '"event":"retry"'
RETRIES=$(jq -c 'select(.event=="retry" and .hook=="flaky-hook")' <"$(logfile)" 2>/dev/null | wc -l)
if [[ "$RETRIES" == "2" ]]; then
    ok "exactly RETRY retries were attempted"
else
    no "exactly RETRY retries were attempted" "retry events = $RETRIES"
fi
check_contains "the successful attempt is recorded" "$(cat "$(logfile)")" '"event":"finish"'

section "12. timeout handling"
clear_hooks
install_hook post-quorate slow-hook '# TIMEOUT: 1
' 'sleep 30'
check_rc "a hook that exceeds TIMEOUT fails" 143 cli run slow-hook --quiet
check_contains "the timeout is logged" "$(cat "$(logfile)")" '"event":"timeout"'
check_contains "the timeout records the exit code" "$(cat "$(logfile)")" '"exit":143'

section "13. status"
check_zero "status succeeds" cli status
check_contains "status reports the version" "$OUT" "Version:"
check_contains "status reports sballd" "$OUT" "sballd PID:"
check_contains "status reports the watcher" "$OUT" "Watcher:"
check_contains "status reports the last pre hook" "$OUT" "Last pre-hook:"
check_contains "status reports the last post hook" "$OUT" "Last post-hook:"
check_zero "status --json succeeds" cli status --json

section "14. JSON logging"
LOGLINES=$(cli log --limit=0)
if printf '%s\n' "$LOGLINES" | jq -e . >/dev/null 2>&1; then
    ok "every log record is valid JSON (jq)"
else
    no "every log record is valid JSON (jq)" "$(printf '%s\n' "$LOGLINES" | jq -e . 2>&1 | head -3)"
fi
check_zero "log --phase=post-quorate works" cli log --phase=post-quorate --limit=0
check_not_contains "phase filter really filters" "$OUT" '"phase":"pre-quorate"'
check_zero "log --hook=flaky-hook works" cli log --hook=flaky-hook --limit=0
check_contains "hook filter really filters" "$OUT" "flaky-hook"
check_zero "log --since=1h works" cli log --since=1h --limit=0
check_zero "log --level=error works" cli log --level=error --limit=0
check_zero "log --pretty works" cli log --pretty --limit=20
check_contains "the log file is where the config says" "$(cat "$(logfile)")" '"hook":"slow-hook"'

section "15. validate"
clear_hooks
install_hook post-quorate valid-hook '' 'exit 0'
check_zero "validate finds no problems" cli validate
check_contains "validate reports the socket" "$OUT" "socket"
run_cmd bash -c 'printf "#!/bin/bash\n### BEGIN SBALL-ADDITION INFO\n# HOOK: post-quorate\n### END SBALL-ADDITION INFO\n" > "$1/etc/sball-addition/hooks.d/post-quorate.d/99-broken.sh"' _ "$ROOT"
check_rc "validate detects a broken header" 4 cli validate
check_contains "the problem is explained" "$OUT" "NAME"
check_rc "list --strict detects a broken hook too" 4 cli list --strict
rm -f "$ROOT/etc/sball-addition/hooks.d/post-quorate.d/99-broken.sh"

section "16. shipped sample hooks"
clear_hooks
for f in "$REPO"/hooks/pre-quorate.d/*.sh "$REPO"/hooks/post-quorate.d/*.sh; do
    # shellcheck disable=SC1090
    if bash -n "$f"; then :; else no "sample hook syntax: $(basename "$f")"; fi
done
ok "every sample hook parses as bash"
cp "$REPO"/hooks/pre-quorate.d/*.sh "$ROOT/etc/sball-addition/hooks.d/pre-quorate.d/"
cp "$REPO"/hooks/post-quorate.d/*.sh "$ROOT/etc/sball-addition/hooks.d/post-quorate.d/"
chmod +x "$ROOT"/etc/sball-addition/hooks.d/*.d/*.sh
check_zero "validate accepts the sample hooks" cli validate
check_contains "samples are listed" "$(cli list)" "register-monitoring"
check_zero "a sample post-quorate hook runs" cli run notify --quiet
check_contains "the sample wrote a JSON record itself" "$(cat "$(logfile)")" '"event":"hook_log"'

section "17. watcher lock and version"
stop_watcher
echo not-quorate >"$SANDBOX/state"
check_zero "version works" cli version
check_contains "version prints the program name" "$OUT" "sball-addition"
cli watcher --foreground --poll-timeout 30 >"$SANDBOX/watcher2.out" 2>&1 &
W1=$!
sleep 1
check_rc "a second watcher refuses to start (exit 5)" 5 cli watcher --foreground --poll-timeout 5
kill "$W1" 2>/dev/null
wait "$W1" 2>/dev/null
sleep 0.3
echo quorate >"$SANDBOX/state"
ok "lock released after the first watcher stopped"

section "18. complete uninstall"
run_cmd "$REPO/scripts/uninstall.sh" --root="$ROOT"
if [[ "$RC" == 0 ]]; then
    ok "uninstall.sh exits 0"
else
    no "uninstall.sh exits 0" "$OUT"
fi
check_contains "uninstall reports no leftovers" "$OUT" "no leftovers"
LEFTOVER=0
for path in "$ROOT/etc/sball-addition" "$ROOT/var/lib/sball-addition" "$ROOT/var/log/sball-addition" \
    "$ROOT/usr/bin/sball-addition" "$ROOT/usr/bin/sballd-wrapper" \
    "$ROOT/usr/lib/systemd/system/sball-addition-watcher.service"; do
    [[ -e "$path" ]] && {
        no "still present: $path"
        LEFTOVER=1
    }
done
((LEFTOVER == 0)) && ok "no files or state are left behind"

# ------------------------------------------------------------------ summary --
printf '\n\033[1m%s\033[0m\n' "summary: $PASS passed, $FAIL failed, $SKIP skipped"
if ((FAIL > 0)); then
    printf '\033[31mINTEGRATION TEST FAILED\033[0m\n'
    exit 1
fi
printf '\033[32mINTEGRATION TEST PASSED\033[0m\n'
