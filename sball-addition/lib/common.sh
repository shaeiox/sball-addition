# shellcheck shell=bash
#
# common.sh - helper library shared by sball-addition hooks.
#
# Sourcing this file is optional but recommended: it gives hooks JSON logging
# into the same log file as the runner, plus a few small helpers that make hook
# scripts idempotent and readable.
#
#   #!/bin/bash
#   ### BEGIN SBALL-ADDITION INFO
#   # HOOK: post-quorate
#   # NAME: my-hook
#   ### END SBALL-ADDITION INFO
#   set -euo pipefail
#   source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"
#   sball_info "hello from ${SBALL_HOSTNAME}"
#
# Environment provided by the runner:
#   SBALL_HOOK_PHASE SBALL_HOOK_NAME SBALL_HOSTNAME SBALL_MASTER_ID
#   SBALL_IS_MASTER SBALL_QUORATE_STATE SBALL_LOG_FILE SBALL_HOOK_PATH
#   SBALL_STATE_DIR SBALL_STATE_FILE SBALL_RUNTIME_DIR SBALL_RUN_ID
#   SBALL_RETRY_ATTEMPT SBALL_RETRY_MAX SBALL_ADDITION_VERSION

: "${SBALL_LOG_FILE:=/var/log/sball-addition/sball-addition.log}"
: "${SBALL_HOOK_PHASE:=unknown}"
: "${SBALL_HOOK_NAME:=${0##*/}}"
: "${SBALL_STATE_DIR:=/var/lib/sball-addition/state/${SBALL_HOOK_PHASE}}"

# _sball_json_escape <string> - make a string safe for a JSON string value.
_sball_json_escape() {
    local s=$1
    s=${s//\\/\\\\}
    s=${s//\"/\\\"}
    s=${s//$'\r'/ }
    s=${s//$'\n'/ }
    s=${s//$'\t'/ }
    printf '%s' "$s"
}

# sball_log <level> <message...> - append a JSON record to $SBALL_LOG_FILE.
sball_log() {
    local level=$1; shift
    local message=$*
    local ts line

    ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    line=$(printf '{"ts":"%s","level":"%s","pid":%d,"phase":"%s","hook":"%s","event":"hook_log","message":"%s"}' \
        "$ts" "$level" "$$" \
        "$(_sball_json_escape "$SBALL_HOOK_PHASE")" \
        "$(_sball_json_escape "$SBALL_HOOK_NAME")" \
        "$(_sball_json_escape "$message")")

    if printf '%s\n' "$line" >>"$SBALL_LOG_FILE" 2>/dev/null; then
        :
    else
        printf '%s: %s\n' "$SBALL_HOOK_NAME" "$message" >&2
    fi

    if [[ -n "${SBALL_HOOK_VERBOSE:-}" ]]; then
        printf '[%s] %s: %s\n' "$level" "$SBALL_HOOK_NAME" "$message" >&2
    fi
}

sball_debug() {
    if [[ -n "${SBALL_HOOK_DEBUG:-}" ]]; then
        sball_log debug "$@"
    fi
}
sball_info() { sball_log info "$@"; }
sball_warn() { sball_log warn "$@"; }
sball_error() { sball_log error "$@"; }

# sball_have <command> - true when the command exists.
sball_have() { command -v "$1" >/dev/null 2>&1; }

# sball_require <command...> - fail when any command is missing.
sball_require() {
    local c
    for c in "$@"; do
        if ! sball_have "$c"; then
            sball_error "required command not found: $c"
            return 1
        fi
    done
}

# sball_is_true <value> - tolerant boolean test.
sball_is_true() {
    case "${1,,}" in
        1 | true | yes | on | enabled) return 0 ;;
        *) return 1 ;;
    esac
}

# sball_is_master - true when this node is the cluster master.
sball_is_master() { [[ "${SBALL_IS_MASTER:-false}" == "true" ]]; }

# sball_state_file [name] - path of a persistent marker for this hook.
sball_state_file() { printf '%s/%s' "$SBALL_STATE_DIR" "${1:-$SBALL_HOOK_NAME}.state"; }

# sball_mark_state [name] - create the marker directory/file.
sball_mark_state() {
    local f
    f=$(sball_state_file "${1:-}")
    mkdir -p "$(dirname "$f")"
    printf '%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$f"
}

# sball_retry <attempts> <delay_seconds> <command...> - retry a command.
sball_retry() {
    local attempts=$1 delay=$2 rc=1 i=1
    shift 2
    while ((i <= attempts)); do
        if "$@"; then
            return 0
        fi
        rc=$?
        sball_warn "attempt $i/$attempts failed (rc=$rc): $*"
        ((i < attempts)) && sleep "$delay"
        ((i++))
    done
    return "$rc"
}

# sball_run <description> <command...> - run a command, log start/finish.
sball_run() {
    local desc=$1 rc=0
    shift
    sball_info "running: $desc"
    "$@" || rc=$?
    if ((rc == 0)); then
        sball_info "ok: $desc"
    else
        sball_error "failed ($rc): $desc"
    fi
    return "$rc"
}

# sball_mounted <path> - true when path is a mount point.
sball_mounted() { mountpoint -q "$1" 2>/dev/null; }

# sball_wait_for <seconds> <command...> - poll a command until it succeeds.
sball_wait_for() {
    local timeout=$1 rc=1 i=0
    shift
    local deadline=$((SECONDS + timeout))
    while ((SECONDS < deadline)); do
        if "$@"; then
            return 0
        fi
        rc=$?
        sleep 1
        ((i++))
    done
    return "$rc"
}

# sball_env_dump - debug helper: log every SBALL_* variable.
sball_env_dump() {
    local v
    while IFS= read -r v || [[ -n "$v" ]]; do
        sball_debug "$v"
    done < <(env | grep '^SBALL_' | sort)
}
