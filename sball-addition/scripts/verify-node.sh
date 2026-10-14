#!/bin/bash
#
# sball-node-verify - production acceptance check for one node.
#
# It answers the only question that matters during a rollout: "is this node
# really wired up and will the hooks actually run?" - and exits non-zero when
# something that matters is broken.
#
#   sball-node-verify                     # check the local node
#   sball-node-verify --smoke             # + run a temporary hook end to end
#   sball-node-verify --root=/tmp/sandbox # check an installation in a sandbox
#   sball-node-verify --json              # machine readable (CI / rollout)
#
# Exit codes: 0 = healthy (warnings are allowed), 1 = at least one check failed,
# 2 = usage error.
#
# Checks: binary, configuration, hooks, wrapper, systemd wiring, watcher,
# log directory/file, state/runtime permissions, ownership, PVM socket, what the
# pre-quorate phase would run (--dry-run) and optionally a real hook round trip.
#
# Everything here is read-only: this verifier never starts, stops or restarts
# sballd.service. It only reports whether the wrapper is wired into it.
set -uo pipefail

ROOT="${SBALL_ADDITION_ROOT:-/}"
PREFIX=
SMOKE=0
AS_JSON=0
QUIET=0
WATCHER_UNIT="sball-addition-watcher.service"

usage() {
    cat <<'EOF'
usage: sball-node-verify [options]

options:
  --root=DIR     installation root to check (default $SBALL_ADDITION_ROOT or /)
  --prefix=DIR   prefix of the program paths (default $root)
  --smoke        install, run and remove a temporary post-quorate hook
  --json         print the result as JSON
  --quiet        only print problems and the summary
  -h, --help     this help

exit status: 0 healthy, 1 failures, 2 usage error
EOF
}

for arg in "$@"; do
    case "$arg" in
        --root=*) ROOT="${arg#*=}" ;;
        --prefix=*) PREFIX="${arg#*=}" ;;
        --smoke) SMOKE=1 ;;
        --json) AS_JSON=1 ;;
        --quiet) QUIET=1 ;;
        -h | --help) usage; exit 0 ;;
        *)
            echo "sball-node-verify: unknown option: $arg" >&2
            usage >&2
            exit 2
            ;;
    esac
done

ROOT="${ROOT%/}"
[[ -z "$ROOT" ]] && ROOT=/
PREFIX="${PREFIX:-$ROOT}"
PREFIX="${PREFIX%/}"
[[ -z "$PREFIX" ]] && PREFIX=/

BIN="$PREFIX/usr/bin/sball-addition"
WRAPPER_BIN="$PREFIX/usr/bin/sballd-wrapper"
WRAPPER_SBIN="$PREFIX/usr/sbin/sballd-wrapper"
CONF_DIR="$ROOT/etc/sball-addition"
HOOKS_DIR="$CONF_DIR/hooks.d"
STATE_DIR="$ROOT/var/lib/sball-addition/state"
RUNTIME_DIR="$ROOT/var/lib/sball-addition/runtime"
LOG_DIR="$ROOT/var/log/sball-addition"
LOG_FILE="$LOG_DIR/sball-addition.log"
UNIT_DIR="$ROOT/usr/lib/systemd/system"
DROPIN="$ROOT/etc/systemd/system/sballd.service.d/sball-addition.conf"

PASS=0
WARN=0
FAIL=0
JSON_ITEMS=()

json_escape() {
    local s=$1
    s=${s//\\/\\\\}
    s=${s//\"/\\\"}
    s=${s//$'\n'/ }
    s=${s//$'\t'/ }
    printf '%s' "$s"
}

record() { # record <status> <check> <message>
    local status=$1 check=$2 message=$3
    case "$status" in
        ok) PASS=$((PASS + 1)) ;;
        warn) WARN=$((WARN + 1)) ;;
        fail) FAIL=$((FAIL + 1)) ;;
    esac
    JSON_ITEMS+=("{\"check\":\"$(json_escape "$check")\",\"status\":\"$status\",\"message\":\"$(json_escape "$message")\"}")
    if ((AS_JSON)); then
        return 0
    fi
    ((QUIET)) && [[ "$status" == "ok" ]] && return 0
    local marker
    case "$status" in
        ok) marker=$'\033[32mok\033[0m  ' ;;
        warn) marker=$'\033[33mwarn\033[0m' ;;
        fail) marker=$'\033[31mFAIL\033[0m' ;;
    esac
    printf '[%s] %-22s %s\n' "$marker" "$check" "$(printf '%s' "$message" | sed '2,$s/^/       /')"
    if [[ "$status" == "fail" && -n "${RECORD_HINT:-}" ]]; then
        printf '       %s\n' "$RECORD_HINT"
    fi
}

ok() { record ok "$1" "$2"; }
warn() { record warn "$1" "$2"; }
fail() { record fail "$1" "$2"; }

section() {
    ((AS_JSON)) && return 0
    ((QUIET)) || printf '\n\033[1m%s\033[0m\n' "$1"
}

note() { # a plain hint line, never a check
    ((AS_JSON)) && return 0
    ((QUIET)) && return 0
    printf '       %s\n' "$*"
}

cli() { "$BIN" --root="$ROOT" "$@"; }

# ------------------------------------------------------------- 1. binary ----
section "programs"
if [[ -x "$BIN" ]]; then
    if VER=$("$BIN" version 2>/dev/null); then
        ok "binary" "$VER"
    else
        fail "binary" "$BIN runs but 'version' failed"
    fi
else
    fail "binary" "$BIN is missing or not executable"
fi

if [[ -x "$WRAPPER_BIN" || -x "$WRAPPER_SBIN" ]]; then
    wrapper=$([[ -x "$WRAPPER_BIN" ]] && printf '%s' "$WRAPPER_BIN" || printf '%s' "$WRAPPER_SBIN")
    if grep -q 'sball-addition' "$wrapper" 2>/dev/null; then
        ok "wrapper" "$wrapper"
    else
        warn "wrapper" "$wrapper does not look like the sballd-wrapper"
    fi
else
    fail "wrapper" "no sballd-wrapper under $PREFIX/usr/{bin,sbin}"
fi

# the shim (optional, only when an operator applied it on purpose)
if [[ -f "$PREFIX/usr/sbin/sballd" ]] && grep -q "sballd-wrapper" "$PREFIX/usr/sbin/sballd" 2>/dev/null; then
    if [[ -x "$PREFIX/usr/sbin/sballd.real" ]]; then
        ok "shim" "/usr/sbin/sballd is the wrapper, real daemon at /usr/sbin/sballd.real; sballd.service still starts where you start it"
    else
        fail "shim" "/usr/sbin/sballd is the wrapper but /usr/sbin/sballd.real is missing - the service cannot start"
    fi
elif [[ -f "$PREFIX/usr/sbin/sballd.real" ]]; then
    warn "shim" "/usr/sbin/sballd.real exists but /usr/sbin/sballd is not the wrapper (half applied; 'sball-integration remove' cleans it up)"
fi

# -------------------------------------------------------- 2. configuration ---
section "configuration and hooks"
if [[ -f "$CONF_DIR/sball-addition.conf" ]]; then
    ok "config file" "$CONF_DIR/sball-addition.conf"
else
    warn "config file" "no configuration, built-in defaults are in use"
fi

VALIDATE_OUT=$(cli validate 2>&1)
VALIDATE_RC=$?
VERIFY_FAILS=$(printf '%s\n' "$VALIDATE_OUT" | grep -c '^\[FAIL\]')
VERIFY_WARNS=$(printf '%s\n' "$VALIDATE_OUT" | grep -c '^\[warn\]')
if ((VALIDATE_RC == 0)); then
    ok "validate" "no failures ($VERIFY_WARNS warning(s))"
elif ((VALIDATE_RC == 4)); then
    fail "validate" "$VERIFY_FAILS check(s) failed:
$(printf '%s\n' "$VALIDATE_OUT" | grep -E '^\[FAIL\]' | head -5)"
else
    fail "validate" "exited $VALIDATE_RC: $(printf '%s' "$VALIDATE_OUT" | tail -1)"
    RECORD_HINT=""
fi

for phase in pre-quorate post-quorate; do
    dir="$HOOKS_DIR/$phase.d"
    count=0
    [[ -d "$dir" ]] && count=$(find "$dir" -maxdepth 1 -type f -name '*.sh' 2>/dev/null | wc -l)
    if ((count > 0)); then
        ok "hooks $phase" "$count hook(s) in $dir"
    else
        warn "hooks $phase" "no hooks in $dir"
    fi
done

# world/group writable hook files are a privilege escalation risk (hooks run as
# root, so only root may be able to change them)
loose=$(find "$HOOKS_DIR" -maxdepth 2 -type f \( -perm -g+w -o -perm -o+w \) 2>/dev/null | head -5)
if [[ -n "$loose" ]]; then
    fail "hook permissions" "group/world writable hooks found (they execute as root):
$loose"
else
    ok "hook permissions" "no group/world writable hook files"
fi
if [[ -d "$CONF_DIR" ]] && [[ -n "$(find "$CONF_DIR" -maxdepth 0 -perm -o+w 2>/dev/null)" ]]; then
    fail "config permissions" "$CONF_DIR is world writable"
else
    ok "config permissions" "$CONF_DIR is not world writable"
fi

# what the pre-quorate phase would do - the same code path the wrapper runs
DRY_OUT=$(cli run-phase pre-quorate --dry-run --quiet 2>&1)
DRY_RC=$?
WOULD_RUN=$(printf '%s\n' "$DRY_OUT" | grep -c 'would run')
if ((DRY_RC == 0)); then
    ok "pre-quorate plan" "$WOULD_RUN hook(s) would run:
$(printf '%s\n' "$DRY_OUT" | grep 'would run' | sed 's/^/  /')"
else
    fail "pre-quorate plan" "run-phase --dry-run exited $DRY_RC:
$(printf '%s' "$DRY_OUT" | tail -3)"
fi

# ------------------------------------------------------------- 3. systemd ----
section "systemd wiring"
if [[ "$ROOT" == "/" ]] && command -v systemctl >/dev/null 2>&1; then
    # Read-only: the unit files on disk, never "systemctl cat" and never any
    # command that could change the state of sballd.service.
    if grep -q 'sballd-wrapper' "$DROPIN" 2>/dev/null; then
        ok "sballd.service" "starts the hooks through the wrapper (drop-in applied)"
    elif [[ -f "$DROPIN" ]]; then
        warn "sballd.service" "$DROPIN exists but does not point at the wrapper"
    elif grep -q 'sballd-wrapper' "$PREFIX/usr/sbin/sballd" 2>/dev/null; then
        ok "sballd.service" "starts the wrapper through the /usr/sbin/sballd shim"
    else
        ok "sballd.service" "left alone (default): hooks do not run at start yet, sballd.service is untouched"
        note "to change that: sball-integration apply   (then restart the service yourself)"
    fi

    if [[ -f "$UNIT_DIR/$WATCHER_UNIT" ]]; then
        if systemctl is-enabled --quiet "$WATCHER_UNIT" 2>/dev/null; then
            ok "watcher unit" "$WATCHER_UNIT is enabled"
        else
            warn "watcher unit" "$WATCHER_UNIT is installed but not enabled (systemctl enable --now $WATCHER_UNIT)"
        fi
        if systemctl is-active --quiet "$WATCHER_UNIT" 2>/dev/null; then
            ok "watcher service" "active"
        elif [[ -f "$RUNTIME_DIR/watcher.pid" ]] && kill -0 "$(cat "$RUNTIME_DIR/watcher.pid" 2>/dev/null)" 2>/dev/null; then
            ok "watcher service" "running as pid $(cat "$RUNTIME_DIR/watcher.pid") (started by the wrapper)"
        else
            warn "watcher service" "not running - post-quorate hooks will not fire on the next start"
        fi
    else
        fail "watcher unit" "$UNIT_DIR/$WATCHER_UNIT is missing"
    fi
else
    warn "systemd" "skipped (root=$ROOT, systemctl $(command -v systemctl >/dev/null 2>&1 && echo present || echo missing))"
fi

# watcher process regardless of how it was started
WATCHER_PID=0
[[ -f "$RUNTIME_DIR/watcher.pid" ]] && WATCHER_PID=$(cat "$RUNTIME_DIR/watcher.pid" 2>/dev/null || echo 0)
if [[ "$WATCHER_PID" != "0" ]] && kill -0 "$WATCHER_PID" 2>/dev/null; then
    ok "watcher process" "pid $WATCHER_PID is alive"
else
    warn "watcher process" "no watcher process (post-quorate hooks are not scheduled right now)"
fi

# ----------------------------------------------------- 4. files and socket ---
section "paths, log and socket"
for dir in "$STATE_DIR" "$RUNTIME_DIR" "$LOG_DIR"; do
    if [[ ! -d "$dir" ]]; then
        fail "path" "$dir is missing (run the installer)"
        continue
    fi
    probe="$dir/.sball-node-verify"
    if touch "$probe" 2>/dev/null; then
        rm -f "$probe"
        ok "path" "$dir is writable"
    else
        fail "path" "$dir is not writable by $(id -un)"
    fi
done

if [[ -f "$LOG_FILE" ]]; then
    lines=$(wc -l <"$LOG_FILE" 2>/dev/null || echo 0)
    if last=$(tail -1 "$LOG_FILE" 2>/dev/null) && [[ -n "$last" ]]; then
        if printf '%s' "$last" | grep -q '^{'; then
            ok "log file" "$lines JSON record(s), last: $(printf '%s' "$last" | cut -c1-90)"
        else
            warn "log file" "last line is not a JSON record: $(printf '%s' "$last" | cut -c1-90)"
        fi
    else
        warn "log file" "exists but is empty (no hook ran yet)"
    fi
else
    warn "log file" "$LOG_FILE does not exist yet (it is created on the first event)"
fi

if STATUS_OUT=$(cli status 2>&1); then
    socket_line=$(printf '%s\n' "$STATUS_OUT" | grep -m1 'Socket:' | sed 's/^ *//')
    state_line=$(printf '%s\n' "$STATUS_OUT" | grep -m1 'sballd State:' | sed 's/^ *//')
    if printf '%s' "$socket_line" | grep -q '(reachable'; then
        ok "pvm socket" "$socket_line | $state_line"
    else
        warn "pvm socket" "$socket_line (normal while sballd is down)"
    fi
else
    fail "status" "sball-addition status exited $?"
fi

# ---------------------------------------------------------- 5. smoke test ----
if ((SMOKE)); then
    section "smoke test (real hook round trip)"
    smoke_name="zz-verify-smoke"
    marker="$RUNTIME_DIR/verify-smoke.$$"
    tmp_hook=$(mktemp "${TMPDIR:-/tmp}/sball-smoke.XXXXXX.sh")
    cat >"$tmp_hook" <<EOF
#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: $smoke_name
# DESCRIPTION: temporary hook installed by sball-node-verify
# TIMEOUT: 10
# TARGET: local
# ENABLED: true
### END SBALL-ADDITION INFO
echo "\$SBALL_HOOK_NAME \$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$marker"
exit 0
EOF
    chmod +x "$tmp_hook"

    if cli install-hook "$tmp_hook" --phase=post-quorate --force >/dev/null 2>&1; then
        ok "smoke install" "temporary hook installed as $smoke_name"
        if cli run "$smoke_name" --quiet --force >/dev/null 2>&1 && [[ -f "$marker" ]]; then
            ok "smoke run" "hook executed and wrote its marker"
        else
            fail "smoke run" "the hook did not run: $(cli run "$smoke_name" --quiet --force 2>&1 | tail -2)"
        fi
        cli uninstall-hook "$smoke_name" --phase=post-quorate >/dev/null 2>&1 &&
            ok "smoke cleanup" "temporary hook removed" ||
            warn "smoke cleanup" "could not remove the temporary hook, remove it manually"
    else
        fail "smoke install" "could not install a temporary hook into $HOOKS_DIR/post-quorate.d"
    fi
    rm -f "$tmp_hook" "$marker"
fi

# ---------------------------------------------------------------- summary ----
if ((AS_JSON)); then
    joined=$(IFS=,; printf '%s' "${JSON_ITEMS[*]}")
    printf '{"host":"%s","root":"%s","ok":%d,"warnings":%d,"failures":%d,"healthy":%s,"checks":[%s]}\n' \
        "$(json_escape "$(hostname -s 2>/dev/null || hostname)")" "$(json_escape "$ROOT")" \
        "$PASS" "$WARN" "$FAIL" "$([[ $FAIL -eq 0 ]] && echo true || echo false)" "$joined"
else
    printf '\nsball-node-verify: %d ok, %d warning(s), %d failure(s)\n' "$PASS" "$WARN" "$FAIL"
fi

if ((FAIL > 0)); then
    ((AS_JSON)) || printf '\033[31mNODE NOT READY\033[0m - fix the failures above before restarting sballd\n'
    exit 1
fi
((AS_JSON)) || printf '\033[32mNODE READY\033[0m\n'
exit 0
