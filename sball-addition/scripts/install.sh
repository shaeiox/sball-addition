#!/bin/bash
#
# install.sh - one command installation for sball-addition.
#
#   sudo ./scripts/install.sh                       # binary, wrapper, config, samples
#   sudo ./scripts/install.sh --with-systemd        # + systemd units and drop-in
#   sudo ./scripts/install.sh --shim-sballd         # + wrap /usr/sbin/sballd itself
#   ./scripts/install.sh --root=/tmp/test --dry-run # rehearsal
#
# The script is idempotent: files that already exist are kept unless --force is
# given (hooks and the configuration file are never overwritten silently).
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)

ROOT=/
PREFIX=
BINARY=
SAMPLES=auto
FORCE=0
WITH_SYSTEMD=0
ENABLE_SERVICE=0
SHIM_SBALLD=0
DRY_RUN=0
VERBOSE=0

usage() {
    cat <<'EOF'
usage: install.sh [options]

options:
  --root=DIR          treat DIR as the filesystem root for /etc,/var,/usr
                      (used for tests and chroots; default /)
  --prefix=DIR        prefix for the binary/wrapper paths (default $root)
  --binary=FILE       install this prebuilt sball-addition binary
                      (default: build with "make build")
  --with-samples      install the sample hooks (default: only when a phase
                      directory is still empty)
  --no-samples        never install the sample hooks
  --with-systemd      install the watcher unit and the sballd.service drop-in
  --enable-service    enable + start sball-addition-watcher.service
                      (implies --with-systemd)
  --shim-sballd       move the real daemon to /usr/sbin/sballd.real and install
                      the wrapper as /usr/sbin/sballd, so that starts bypassing
                      systemd (e.g. "pvm-wrapper --start") also run the hooks
  --force             overwrite the configuration file and sample hooks
  --dry-run           print what would happen
  -v, --verbose       verbose output
  -h, --help          this help
EOF
}

log() { printf '%s\n' "$*"; }
info() { printf '  %s\n' "$*"; }
die() {
    printf 'install.sh: error: %s\n' "$*" >&2
    exit 1
}
run() {
    if ((DRY_RUN)); then
        printf '  [dry-run] %s\n' "$*"
        return 0
    fi
    ((VERBOSE)) && printf '  + %s\n' "$*"
    "$@"
}

for arg in "$@"; do
    case "$arg" in
        --root=*) ROOT="${arg#*=}" ;;
        --prefix=*) PREFIX="${arg#*=}" ;;
        --binary=*) BINARY="${arg#*=}" ;;
        --with-samples) SAMPLES=yes ;;
        --no-samples) SAMPLES=no ;;
        --with-systemd) WITH_SYSTEMD=1 ;;
        --enable-service) WITH_SYSTEMD=1 ENABLE_SERVICE=1 ;;
        --shim-sballd) SHIM_SBALLD=1 ;;
        --force) FORCE=1 ;;
        --dry-run) DRY_RUN=1 ;;
        -v | --verbose) VERBOSE=1 ;;
        -h | --help) usage; exit 0 ;;
        *) echo "install.sh: unknown option: $arg" >&2; usage >&2; exit 2 ;;
    esac
done

# A --root target is usually a chroot or a sandbox: create it when missing.
if [[ ! -d "$ROOT" ]]; then
    run mkdir -p "$ROOT" || die "root '$ROOT' does not exist and cannot be created"
fi
PREFIX="${PREFIX:-$ROOT}"
ROOT="${ROOT%/}"
PREFIX="${PREFIX%/}"
[[ -z "$ROOT" ]] && ROOT=/
[[ -z "$PREFIX" ]] && PREFIX=/
p() { # p <abs path> -> path inside the target root
    if [[ "$ROOT" == "/" ]]; then printf '%s' "$1"; else printf '%s%s' "$ROOT" "$1"; fi
}

BIN_DIR="$PREFIX/usr/bin"
SBIN_DIR="$PREFIX/usr/sbin"
CONF_DIR=$(p "/etc/sball-addition")
LIB_DIR="$CONF_DIR/lib"
PRE_DIR="$CONF_DIR/hooks.d/pre-quorate.d"
POST_DIR="$CONF_DIR/hooks.d/post-quorate.d"
STATE_DIR=$(p "/var/lib/sball-addition/state")
RUNTIME_DIR=$(p "/var/lib/sball-addition/runtime")
LOG_DIR=$(p "/var/log/sball-addition")
UNIT_DIR=$(p "/usr/lib/systemd/system")
DROPIN_DIR=$(p "/etc/systemd/system/sballd.service.d")

log "sball-addition installer"
info "root:        $ROOT"
info "prefix:      $PREFIX"
info "hooks:       $CONF_DIR/hooks.d"
info "state:       $STATE_DIR"

# ---------------------------------------------------------------- build -----
if [[ -z "$BINARY" ]]; then
    if command -v make >/dev/null 2>&1 && command -v go >/dev/null 2>&1; then
        log ""
        log "building the binary (make build)"
        run make -C "$REPO_DIR" build
        BINARY="$REPO_DIR/bin/sball-addition"
    else
        die "neither --binary=FILE nor a Go toolchain was provided"
    fi
fi
[[ -f "$BINARY" ]] || die "binary '$BINARY' not found"

# ------------------------------------------------------------ directories ---
log ""
log "creating directories"
run install -d -m 0755 "$BIN_DIR" "$SBIN_DIR"
run install -d -m 0755 "$CONF_DIR" "$LIB_DIR" "$CONF_DIR/hooks.d" "$PRE_DIR" "$POST_DIR"
run install -d -m 0755 "$(p "/var/lib/sball-addition")" "$STATE_DIR" \
    "$STATE_DIR/pre-quorate" "$STATE_DIR/post-quorate" "$RUNTIME_DIR"
run install -d -m 0755 "$LOG_DIR"

# ---------------------------------------------------------------- files -----
log ""
log "installing the binary and the wrapper"
run install -m 0755 "$BINARY" "$BIN_DIR/sball-addition"
run install -m 0755 "$REPO_DIR/scripts/sballd-wrapper" "$BIN_DIR/sballd-wrapper"
if [[ "$SBIN_DIR" != "$BIN_DIR" ]]; then
    run install -m 0755 "$REPO_DIR/scripts/sballd-wrapper" "$SBIN_DIR/sballd-wrapper"
fi
run install -m 0644 "$REPO_DIR/lib/common.sh" "$LIB_DIR/common.sh"

log ""
log "installing the configuration"
if [[ -f "$CONF_DIR/sball-addition.conf" && $FORCE -eq 0 ]]; then
    info "keeping the existing $CONF_DIR/sball-addition.conf (use --force to replace)"
else
    if [[ -f "$CONF_DIR/sball-addition.conf" ]]; then
        run install -m 0644 "$CONF_DIR/sball-addition.conf" "$CONF_DIR/sball-addition.conf.rpmsave"
        info "previous configuration saved as sball-addition.conf.rpmsave"
    fi
    run install -m 0644 "$REPO_DIR/etc/sball-addition.conf" "$CONF_DIR/sball-addition.conf"
fi
run install -d -m 0755 "$(p "/etc/sysconfig")"
if [[ ! -f "$(p "/etc/sysconfig/sball-addition")" ]]; then
    run install -m 0644 "$REPO_DIR/packaging/sysconfig/sball-addition" \
        "$(p "/etc/sysconfig/sball-addition")"
fi
run install -d -m 0755 "$(p "/etc/logrotate.d")"
run install -m 0644 "$REPO_DIR/packaging/logrotate.d/sball-addition" \
    "$(p "/etc/logrotate.d/sball-addition")"

# --------------------------------------------------------------- samples ----
samples_wanted=0
case "$SAMPLES" in
    yes) samples_wanted=1 ;;
    auto)
        shopt -s nullglob
        existing=("$PRE_DIR"/*.sh "$POST_DIR"/*.sh)
        shopt -u nullglob
        [[ ${#existing[@]} -eq 0 ]] && samples_wanted=1
        ;;
esac
if ((samples_wanted)); then
    log ""
    log "installing sample hooks (safe defaults, --no-samples to skip)"
    for f in "$REPO_DIR"/hooks/pre-quorate.d/*.sh; do
        [[ -e "$f" ]] || continue
        base=$(basename "$f")
        if [[ -f "$PRE_DIR/$base" && $FORCE -eq 0 ]]; then
            info "keeping $PRE_DIR/$base"
        else
            run install -m 0755 "$f" "$PRE_DIR/$base"
        fi
    done
    for f in "$REPO_DIR"/hooks/post-quorate.d/*.sh; do
        [[ -e "$f" ]] || continue
        base=$(basename "$f")
        if [[ -f "$POST_DIR/$base" && $FORCE -eq 0 ]]; then
            info "keeping $POST_DIR/$base"
        else
            run install -m 0755 "$f" "$POST_DIR/$base"
        fi
    done
else
    log ""
    info "sample hooks not installed (existing hooks found or --no-samples)"
fi

# -------------------------------------------------------------- systemd -----
if ((WITH_SYSTEMD)); then
    log ""
    log "installing systemd units"
    run install -d -m 0755 "$UNIT_DIR" "$DROPIN_DIR"
    run install -m 0644 "$REPO_DIR/packaging/systemd/sball-addition-watcher.service" \
        "$UNIT_DIR/sball-addition-watcher.service"
    run install -m 0644 "$REPO_DIR/packaging/systemd/sballd.service.d/sball-addition.conf" \
        "$DROPIN_DIR/sball-addition.conf"
fi

if ((SHIM_SBALLD)); then
    log ""
    log "installing the sballd shim"
    sballd="$SBIN_DIR/sballd"
    real="$SBIN_DIR/sballd.real"
    if [[ -f "$real" ]]; then
        info "$real already exists, keeping it"
    elif [[ -f "$sballd" ]] && ! grep -q "sballd-wrapper" "$sballd" 2>/dev/null; then
        run mv "$sballd" "$real"
        info "real daemon moved to $real"
    else
        info "no real daemon at $sballd (nothing to shim)"
    fi
    run install -m 0755 "$REPO_DIR/scripts/sballd-wrapper" "$sballd"
fi

# ------------------------------------------------------------- validation ---
log ""
log "validating the installation"
if ((DRY_RUN)); then
    info "[dry-run] $BIN_DIR/sball-addition --root=${ROOT%/} validate"
else
    if [[ "$ROOT" == "/" ]]; then
        "$BIN_DIR/sball-addition" validate || true
    else
        "$BIN_DIR/sball-addition" --root="$ROOT" validate || true
    fi
fi

# --------------------------------------------------------------- service ----
if ((ENABLE_SERVICE)) && [[ "$ROOT" == "/" ]] && command -v systemctl >/dev/null 2>&1; then
    log ""
    log "enabling the watcher service"
    run systemctl daemon-reload
    run systemctl enable --now sball-addition-watcher.service
fi

log ""
log "done."
info "configuration : $CONF_DIR/sball-addition.conf"
info "hooks         : $PRE_DIR, $POST_DIR"
info "state         : $STATE_DIR"
info "log           : $LOG_DIR/sball-addition.log"
info "next steps:"
info "  sball-addition list"
info "  sball-addition status"
info "  sball-addition validate"
if ((WITH_SYSTEMD)); then
    info "  systemctl daemon-reload && systemctl restart sballd.service"
else
    info "  (re-)run with --with-systemd to point sballd.service at the wrapper"
fi
