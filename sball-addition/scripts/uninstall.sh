#!/bin/bash
#
# uninstall.sh - complete removal of sball-addition.
#
#   sudo ./scripts/uninstall.sh                     # remove everything
#   sudo ./scripts/uninstall.sh --keep-config --keep-state
#   ./scripts/uninstall.sh --root=/tmp/test --dry-run
#
# The default removes the binary, wrapper, hooks, configuration, systemd units,
# state, runtime files and logs - and then proves that nothing is left, so the
# "no leftover artifacts" acceptance criterion is verifiable.
set -euo pipefail

ROOT=/
PREFIX=
KEEP_CONFIG=0
KEEP_STATE=0
KEEP_LOGS=0
KEEP_HOOKS=0
KEEP_SERVICES=0
DRY_RUN=0
VERBOSE=0

usage() {
    cat <<'EOF'
usage: uninstall.sh [options]

options:
  --root=DIR        filesystem root to clean (default /)
  --prefix=DIR      prefix of the binary/wrapper paths (default $root)
  --keep-config     keep /etc/sball-addition (and the sysconfig file)
  --keep-hooks      keep the hook scripts (implies --keep-config)
  --keep-state      keep /var/lib/sball-addition (state + runtime)
  --keep-logs       keep /var/log/sball-addition
  --keep-services   do not touch systemd units or the sballd.service drop-in
  --dry-run         only print what would be removed
  -v, --verbose     verbose output
  -h, --help        this help
EOF
}

log() { printf '%s\n' "$*"; }
info() { printf '  %s\n' "$*"; }
warn() { printf '  warning: %s\n' "$*" >&2; }
run() {
    if ((DRY_RUN)); then
        printf '  [dry-run] %s\n' "$*"
        return 0
    fi
    ((VERBOSE)) && printf '  + %s\n' "$*"
    "$@"
}
rm_if_exists() {
    local path=$1
    if [[ -e "$path" || -L "$path" ]]; then
        run rm -rf -- "$path"
        return 0
    fi
    return 1
}

for arg in "$@"; do
    case "$arg" in
        --root=*) ROOT="${arg#*=}" ;;
        --prefix=*) PREFIX="${arg#*=}" ;;
        --keep-config) KEEP_CONFIG=1 ;;
        --keep-hooks) KEEP_HOOKS=1 KEEP_CONFIG=1 ;;
        --keep-state) KEEP_STATE=1 ;;
        --keep-logs) KEEP_LOGS=1 ;;
        --keep-services) KEEP_SERVICES=1 ;;
        --dry-run) DRY_RUN=1 ;;
        -v | --verbose) VERBOSE=1 ;;
        -h | --help) usage; exit 0 ;;
        *) echo "uninstall.sh: unknown option: $arg" >&2; usage >&2; exit 2 ;;
    esac
done

[[ -d "$ROOT" ]] || {
    echo "uninstall.sh: root '$ROOT' does not exist" >&2
    exit 1
}
PREFIX="${PREFIX:-$ROOT}"
ROOT="${ROOT%/}"
PREFIX="${PREFIX%/}"
[[ -z "$ROOT" ]] && ROOT=/
[[ -z "$PREFIX" ]] && PREFIX=/
p() {
    if [[ "$ROOT" == "/" ]]; then printf '%s' "$1"; else printf '%s%s' "$ROOT" "$1"; fi
}

BIN_DIR="$PREFIX/usr/bin"
SBIN_DIR="$PREFIX/usr/sbin"
CONF_DIR=$(p "/etc/sball-addition")
VAR_DIR=$(p "/var/lib/sball-addition")
LOG_DIR=$(p "/var/log/sball-addition")
UNIT_FILE=$(p "/usr/lib/systemd/system/sball-addition-watcher.service")
DROPIN_DIR=$(p "/etc/systemd/system/sballd.service.d")
DROPIN="$DROPIN_DIR/sball-addition.conf"

log "sball-addition uninstaller"
info "root: $ROOT"

# --------------------------------------------------------------- systemd ----
if ((KEEP_SERVICES == 0)); then
    log ""
    log "stopping and removing systemd units"
    if [[ "$ROOT" == "/" ]] && command -v systemctl >/dev/null 2>&1; then
        run systemctl stop sball-addition-watcher.service || true
        run systemctl disable sball-addition-watcher.service || true
    fi
    rm_if_exists "$UNIT_FILE" || info "no watcher unit at $UNIT_FILE"
    if rm_if_exists "$DROPIN"; then
        info "removed $DROPIN (sballd.service starts the plain daemon again after daemon-reload)"
    else
        info "no sballd drop-in at $DROPIN"
    fi
    if [[ "$ROOT" == "/" ]] && command -v systemctl >/dev/null 2>&1; then
        run systemctl daemon-reload || true
        if systemctl is-active --quiet sballd.service 2>/dev/null; then
            warn "sballd.service is running through the wrapper; restart it to drop the wrapper"
        fi
    fi
fi

# ------------------------------------------------------------------ shim ----
log ""
log "checking for the sballd shim"
sballd="$SBIN_DIR/sballd"
real="$SBIN_DIR/sballd.real"
if [[ -f "$sballd" ]] && grep -q "sballd-wrapper" "$sballd" 2>/dev/null; then
    if [[ -f "$real" ]]; then
        run rm -f -- "$sballd"
        run mv "$real" "$sballd"
        info "restored the real daemon to $sballd"
    else
        warn "$sballd is the wrapper but $real does not exist; not touching it"
    fi
else
    info "no shim installed"
fi

# ----------------------------------------------------------------- files ----
log ""
log "removing programs"
rm_if_exists "$BIN_DIR/sball-addition" || info "no binary at $BIN_DIR/sball-addition"
rm_if_exists "$BIN_DIR/sballd-wrapper" || true
rm_if_exists "$SBIN_DIR/sballd-wrapper" || true
rm_if_exists "$(p "/etc/logrotate.d/sball-addition")" || true
rm_if_exists "$(p "/etc/sysconfig/sball-addition")" || true

if ((KEEP_CONFIG == 0)); then
    log ""
    log "removing configuration and hooks"
    rm_if_exists "$CONF_DIR" || info "no configuration at $CONF_DIR"
else
    log ""
    info "keeping $CONF_DIR (--keep-config)"
    if ((KEEP_HOOKS == 0)); then
        rm_if_exists "$CONF_DIR/hooks.d" || true
    fi
fi

if ((KEEP_STATE == 0)); then
    log ""
    log "removing state, runtime files and RUN_ONCE markers"
    rm_if_exists "$VAR_DIR" || info "no state at $VAR_DIR"
else
    log ""
    info "keeping $VAR_DIR (--keep-state)"
fi

if ((KEEP_LOGS == 0)); then
    log ""
    log "removing logs"
    rm_if_exists "$LOG_DIR" || info "no logs at $LOG_DIR"
else
    log ""
    info "keeping $LOG_DIR (--keep-logs)"
fi

# ------------------------------------------------------------- leftovers ----
log ""
log "checking for leftovers"
leftovers=()
for path in \
    "$BIN_DIR/sball-addition" \
    "$BIN_DIR/sballd-wrapper" \
    "$SBIN_DIR/sballd-wrapper" \
    "$CONF_DIR" \
    "$VAR_DIR" \
    "$LOG_DIR" \
    "$UNIT_FILE" \
    "$DROPIN"; do
    [[ -e "$path" || -L "$path" ]] && leftovers+=("$path")
done
if [[ "$ROOT" == "/" ]]; then
    for extra in /etc/sball-addition /var/lib/sball-addition /var/log/sball-addition \
        /etc/systemd/system/sballd.service.d/sball-addition.conf; do
        [[ -e "$extra" ]] && ! printf '%s\n' "${leftovers[@]:-}" | grep -qx "$extra" && leftovers+=("$extra")
    done
fi

if ((${#leftovers[@]} == 0)); then
    log "no leftovers - uninstall is complete."
else
    warn "the following paths were kept on purpose or could not be removed:"
    for path in "${leftovers[@]}"; do
        warn "  $path"
    done
fi
if ((KEEP_CONFIG == 0 && KEEP_STATE == 0 && KEEP_LOGS == 0 && KEEP_HOOKS == 0)); then
    info "reminder: remove the sball-addition.spec RPM first when it was used (rpm -e sball-addition)"
fi
