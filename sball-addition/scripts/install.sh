#!/bin/bash
#
# install.sh - one command installation for sball-addition.
#
#   sudo ./scripts/install.sh                       # binary, wrapper, config, samples
#   sudo ./scripts/install.sh --with-systemd        # + watcher/metrics units
#   ./scripts/install.sh --root=/tmp/test --dry-run # rehearsal
#
# The script is idempotent: files that already exist are kept unless --force is
# given (hooks and the configuration file are never overwritten silently).
#
# sballd.service is never touched: this installer writes no unit file, no
# drop-in and no binary that belongs to the daemon, and it restarts nothing.
# Wiring the wrapper into the service is a deliberate operator action:
#   sudo sball-integration apply
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
DRY_RUN=0
VERBOSE=0

usage() {
    cat <<'EOF'
usage: install.sh [options]

options:
  --root=DIR          treat DIR as the filesystem root for /etc,/var,/usr
                      (used for tests and chroots; default /)
  --prefix=DIR        where /etc, /var, /usr and the units live (default $root);
                      combine with --root=/ to install into a directory while
                      keeping the systemd integration of the real system
  --binary=FILE       install this prebuilt sball-addition binary
                      (default: build with "make build")
  --with-samples      install the sample hooks (default: only when a phase
                      directory is still empty)
  --no-samples        never install the sample hooks
  --with-systemd      install the sball-addition systemd units (watcher and
                      metrics). Never a unit for sballd.service: the hook
                      system does not reconfigure the daemon it observes.
  --enable-service    enable + start sball-addition-watcher.service
                      (implies --with-systemd)
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
p() { # p <abs path> -> path inside the install prefix
    # PREFIX defaults to ROOT, so this is the same as $ROOT$1 unless --prefix was
    # given. With "--root=/ --prefix=/tmp/x" the files land in /tmp/x while the
    # systemd integration paths (and --enable-service) still refer to the real
    # system, which is what the contract test uses.
    printf '%s%s' "$PREFIX" "$1"
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

log "sball-addition installer"
info "root:        $ROOT"
info "prefix:      $PREFIX"
info "hooks:       $CONF_DIR/hooks.d"
info "state:       $STATE_DIR"

# ---------------------------------------------------------------- build -----
if [[ -z "$BINARY" ]]; then
    # A release tarball ships a prebuilt, statically linked binary: use it so a
    # production node needs neither Go nor make.
    if [[ -x "$REPO_DIR/bin/sball-addition" ]]; then
        log ""
        log "using the prebuilt binary from the release ($REPO_DIR/bin/sball-addition)"
        BINARY="$REPO_DIR/bin/sball-addition"
    elif command -v make >/dev/null 2>&1 && command -v go >/dev/null 2>&1; then
        log ""
        log "building the binary (make build)"
        run make -C "$REPO_DIR" build
        BINARY="$REPO_DIR/bin/sball-addition"
    else
        die "neither --binary=FILE nor a prebuilt bin/sball-addition nor a Go toolchain was provided"
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

# ----------------------------------------------------------------- man ------
log ""
log "installing the man pages"
MAN_DIR="$PREFIX/usr/share/man/man8"
run install -d -m 0755 "$MAN_DIR"
for page in sball-addition sballd-wrapper sball-node-verify sball-rollout sball-integration; do
    src="$REPO_DIR/docs/$page.8"
    [[ -f "$src" ]] || continue
    if command -v gzip >/dev/null 2>&1; then
        if ((DRY_RUN)); then
            info "[dry-run] gzip -9n -c $src > $MAN_DIR/$page.8.gz"
        else
            gzip -9n -c "$src" >"$MAN_DIR/$page.8.gz"
            chmod 0644 "$MAN_DIR/$page.8.gz"
        fi
    else
        run install -m 0644 "$src" "$MAN_DIR/$page.8"
    fi
done

# ---------------------------------------------------------------- files -----
log ""
log "installing the binary and the wrapper"
run install -m 0755 "$BINARY" "$BIN_DIR/sball-addition"
run install -m 0755 "$REPO_DIR/scripts/sballd-wrapper" "$BIN_DIR/sballd-wrapper"
run install -m 0755 "$REPO_DIR/scripts/verify-node.sh" "$BIN_DIR/sball-node-verify"
run install -m 0755 "$REPO_DIR/scripts/rollout.sh" "$BIN_DIR/sball-rollout"
[[ -f "$REPO_DIR/scripts/sballd-integration.sh" ]] && \
    run install -m 0755 "$REPO_DIR/scripts/sballd-integration.sh" "$BIN_DIR/sball-integration"
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
    info "only sball-addition units: sballd.service is not modified"
    run install -d -m 0755 "$UNIT_DIR"
    run install -m 0644 "$REPO_DIR/packaging/systemd/sball-addition-watcher.service" \
        "$UNIT_DIR/sball-addition-watcher.service"
    run install -m 0644 "$REPO_DIR/packaging/systemd/sball-addition-metrics.service" \
        "$UNIT_DIR/sball-addition-metrics.service"
    run install -m 0644 "$REPO_DIR/packaging/systemd/sball-addition-metrics.timer" \
        "$UNIT_DIR/sball-addition-metrics.timer"
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
info "  sball-node-verify --smoke      # is this node ready for a restart?"
if ((ENABLE_SERVICE)); then
    info "  systemctl status sball-addition-watcher.service"
fi
log ""
info "sballd.service is untouched: nothing was installed, changed or restarted."
info "The hooks run while systemd starts the daemon through the wrapper, which is"
info "a decision you make explicitly (it is the only thing that ever writes a"
info "file belonging to the service):"
info "  sudo sball-integration status          # is it applied?"
info "  sudo sball-integration apply           # write the ExecStart drop-in"
info "  sudo systemctl daemon-reload && sudo systemctl restart sballd.service"
info "Until then everything works read-only: observe the node and run hooks on"
info "demand with 'sball-addition run-phase pre-quorate|post-quorate'."
