#!/bin/bash
#
# sballd-integration.sh - the ONLY sball-addition component that writes a file
# that belongs to the sballd service.
#
# Every other part of this project treats sballd.service as read-only: nothing
# starts, stops, restarts, enables, disables or reconfigures it. This script is
# the deliberate, documented exception, and it is only ever run by hand:
#
#   * install.sh, sball-rollout, sball-node-verify and the systemd units of
#     sball-addition never call it,
#   * the RPM/DEB maintainer scripts never call it,
#   * it never calls systemctl itself - it writes the file and prints the
#     command the administrator has to run.
#
# Keep it that way: it is the boundary that lets the hook system be installed on
# a production node without touching the running service.
#
# Usage:
#   sudo sball-integration status                    # is the integration applied?
#   sudo sball-integration apply --mode=dropin       # recommend: ExecStart drop-in
#   sudo sball-integration apply --mode=shim --yes   # also cover non-systemd starts
#   sudo sball-integration remove                    # undo
#   sudo sball-integration apply --dry-run           # show the file, change nothing
set -euo pipefail

PROG=${0##*/}
ROOT=/
PREFIX=
MODE=dropin
ACTION=status
DRY_RUN=0
ASSUME_YES=0
VERBOSE=0

usage() {
    cat <<'EOF'
usage: sball-integration [status|apply|remove] [options]

sball-addition never touches sballd.service. The hooks are run by a wrapper
around the daemon, and wiring that wrapper up is a decision only an
administrator can make - that is what this command does. It writes the file and
tells you the command to run; it never runs systemctl itself.

actions:
  status              report whether the integration is applied (default)
  apply               write the drop-in (or install the shim)
  remove              undo it

options:
  --mode=dropin       /etc/systemd/system/sballd.service.d/sball-addition.conf
                      overrides ExecStart so systemd runs the wrapper (default,
                      reversible, survives PVM updates of sballd.service)
  --mode=shim         move /usr/sbin/sballd to /usr/sbin/sballd.real and install
                      the wrapper as /usr/sbin/sballd, so starts that bypass
                      systemd (for example "pvm-wrapper --start") also run the
                      pre-quorate hooks
  --yes               skip the confirmation prompt (needed for --mode=shim)
  --root=DIR          treat DIR as the filesystem root (tests, chroots)
  --prefix=DIR        prefix for /usr paths (default: $root)
  --print             with 'apply', write nothing: print the file to stdout
  --dry-run           print what would happen, change nothing
  -v, --verbose       show every command
  -h, --help          this help

after 'apply' you run (sball-addition will not do it for you):
  systemctl daemon-reload && systemctl restart sballd.service
EOF
}

log() { printf '%s\n' "$*"; }
info() { printf '  %s\n' "$*"; }
warn() { printf '%s: warning: %s\n' "$PROG" "$*" >&2; }
err() { printf '%s: error: %s\n' "$PROG" "$*" >&2; }
die() {
    err "$*"
    exit 2
}
run() {
    if ((DRY_RUN)); then
        printf '  [dry-run] %s\n' "$*"
        return 0
    fi
    ((VERBOSE)) && printf '+ %s\n' "$*" >&2
    "$@"
}

while (($#)); do
    case "$1" in
        status | apply | remove) ACTION=$1 ;;
        --mode=*) MODE=${1#--mode=} ;;
        --mode)
            shift
            MODE=${1:-}
            ;;
        --root=*) ROOT=${1#--root=} ;;
        --root)
            shift
            ROOT=${1:-}
            ;;
        --prefix=*) PREFIX=${1#--prefix=} ;;
        --prefix)
            shift
            PREFIX=${1:-}
            ;;
        --yes | -y) ASSUME_YES=1 ;;
        --dry-run) DRY_RUN=1 ;;
        --print) ACTION=print ;;
        -v | --verbose) VERBOSE=1 ;;
        -h | --help)
            usage
            exit 0
            ;;
        *) die "unknown option: $1 (try --help)" ;;
    esac
    shift
done

case "$MODE" in
    dropin | shim) ;;
    *) die "--mode must be dropin or shim, got '$MODE'" ;;
esac

ROOT=${ROOT%/}
[[ -n "$ROOT" ]] || ROOT=/
PREFIX=${PREFIX:-$ROOT}
PREFIX=${PREFIX%/}
# A --root of "/" means a real installation: keep the empty prefix so that paths
# stay absolute.
[[ "$ROOT" == "/" ]] && ROOT=""
p() { printf '%s%s' "$PREFIX" "$1"; }

DROPIN_DIR="$(p /etc/systemd/system/sballd.service.d)"
DROPIN="$DROPIN_DIR/sball-addition.conf"
WRAPPER="$(p /usr/bin/sballd-wrapper)"
SBIN_WRAPPER="$(p /usr/sbin/sballd-wrapper)"
SBIN="$(p /usr/sbin)"
SBALLD="$SBIN/sballd"
SBALLD_REAL="$SBIN/sballd.real"

# The drop-in is generated here and nowhere else: one definition, so an
# installed node and this repository can never disagree.
dropin_content() {
    cat <<EOF
# sball-addition - run the pre-quorate hooks before sballd starts.
#
# Written by $PROG (apply --mode=dropin). Remove it with:
#   $PROG remove
# after which "systemctl daemon-reload" restores the original ExecStart.
#
# sball-addition itself never restarts sballd.service; activating this file is
# an operator decision:
#   systemctl daemon-reload && systemctl restart sballd.service
#
# The empty ExecStart line resets the value from the main unit file.

[Service]
ExecStart=
ExecStart=$(p /usr/bin/sballd-wrapper)
EOF
}

# ------------------------------------------------------------------ state ----
dropin_applied() { [[ -f "$DROPIN" ]]; }
shim_applied() { [[ -f "$SBALLD" ]] && grep -q 'sballd-wrapper' "$SBALLD" 2>/dev/null; }
systemctl_would_use_wrapper() { # read-only: never calls systemctl
    local f
    for f in "$DROPIN" "$(p /usr/lib/systemd/system/sballd.service)" \
        "$(p /etc/systemd/system/sballd.service)" "$(p /usr/lib/systemd/system/sball.service)"; do
        [[ -f "$f" ]] || continue
        if grep -q 'sballd-wrapper' "$f" 2>/dev/null; then
            return 0
        fi
    done
    return 1
}

show_status() {
    local rc=1
    log "sball-addition integration for the sballd service"
    info "root:              ${ROOT:-/}"
    info "mode:              $MODE"
    log ""
    if dropin_applied; then
        info "drop-in:           applied ($DROPIN)"
        rc=0
    else
        info "drop-in:           not applied ($DROPIN)"
    fi
    if shim_applied; then
        info "shim:              applied ($SBALLD runs the wrapper)"
        rc=0
    elif [[ -f "$SBALLD_REAL" ]]; then
        warn "shim:              half applied: $SBALLD_REAL exists but $SBALLD is not the wrapper"
    else
        info "shim:              not applied"
    fi
    log ""
    if ((rc == 0)); then
        info "The hooks run through the wrapper. sball-addition will not restart"
        info "or touch sballd.service: restarting the daemon is your call."
    else
        info "Nothing of sball-addition is wired into the daemon: sballd.service is"
        info "untouched and stays that way until an administrator runs"
        info "  $PROG apply"
        info "Out of the box the tool only observes (PVM socket, process table) and"
        info "runs hooks on demand: sball-addition run-phase pre-quorate|post-quorate"
    fi
    return "$rc"
}

# ------------------------------------------------------------------ apply ----
apply_dropin() {
    if ((DRY_RUN)); then
        log "would write $DROPIN:"
        dropin_content | sed 's/^/  | /'
        log ""
        log "then you would run: systemctl daemon-reload && systemctl restart sballd.service"
        return 0
    fi
    run install -d -m 0755 "$DROPIN_DIR"
    if [[ -f "$DROPIN" ]] && ! diff -q <(dropin_content) "$DROPIN" >/dev/null 2>&1; then
        if ((ASSUME_YES)); then
            warn "overwriting the existing $DROPIN"
        else
            die "$DROPIN exists with different content; inspect it and re-run with --yes to replace it"
        fi
    fi
    dropin_content >"$DROPIN.tmp.$$"
    chmod 0644 "$DROPIN.tmp.$$"
    mv -f "$DROPIN.tmp.$$" "$DROPIN"
    log ""
    log "wrote $DROPIN"
    log ""
    log "sball-addition does not restart services. To activate it, run:"
    log "  systemctl daemon-reload && systemctl restart sballd.service"
    log ""
    log "Before that (while the daemon still runs without hooks):"
    log "  sball-addition validate && sball-addition run-phase pre-quorate --dry-run"
    log "  sball-node-verify --smoke"
}

apply_shim() {
    local wrapper_src
    if [[ -x "$SBIN_WRAPPER" ]]; then
        wrapper_src=$SBIN_WRAPPER
    elif [[ -x "$WRAPPER" ]]; then
        wrapper_src=$WRAPPER
    else
        die "the wrapper is not installed ($SBIN_WRAPPER); run the sball-addition installer first"
    fi
    run install -d -m 0755 "$SBIN"
    if [[ -f "$SBALLD_REAL" ]]; then
        info "$SBALLD_REAL already exists, keeping it (the real daemon is already preserved)"
    elif [[ -f "$SBALLD" ]] && ! shim_applied; then
        run mv "$SBALLD" "$SBALLD_REAL"
        info "real daemon moved to $SBALLD_REAL"
    elif ! [[ -f "$SBALLD" ]]; then
        warn "no daemon at $SBALLD (a node without PVM?); installing the wrapper anyway"
    fi
    run install -m 0755 "$wrapper_src" "$SBALLD"
    log ""
    log "installed the wrapper as $SBALLD"
    log ""
    log "sball-addition does not restart services. The shim takes effect on the"
    log "next start of the daemon, however it is started:"
    log "  systemctl restart sballd.service      # systemd"
    log "  pvm-wrapper --start                   # direct start"
    log ""
    log "Undo at any time with: $PROG remove"
}

# ----------------------------------------------------------------- remove ----
remove_integration() {
    local removed=0
    if dropin_applied; then
        run rm -f -- "$DROPIN"
        info "removed $DROPIN"
        removed=1
    else
        info "no drop-in at $DROPIN"
    fi
    if shim_applied; then
        if [[ -f "$SBALLD_REAL" ]]; then
            run rm -f -- "$SBALLD"
            run mv "$SBALLD_REAL" "$SBALLD"
            info "restored the real daemon to $SBALLD"
        else
            warn "$SBALLD is the wrapper but $SBALLD_REAL does not exist; not touching it"
        fi
        removed=1
    fi
    # The directory may have been created by us and holds nothing else.
    if [[ -d "$DROPIN_DIR" ]] && ! ((DRY_RUN)); then
        rmdir "$DROPIN_DIR" 2>/dev/null || true
    fi
    if ((removed)); then
        log ""
        log "sball-addition does not restart services. The change takes effect for"
        log "the next start of the daemon. If you want it now, it is your call:"
        log "  systemctl daemon-reload && systemctl restart sballd.service"
    fi
    return 0
}

case "$ACTION" in
    status) show_status ;;
    print)
        dropin_content
        ;;
    apply)
        if [[ "$MODE" == "dropin" ]]; then apply_dropin; else apply_shim; fi
        ;;
    remove) remove_integration ;;
    *) die "unknown action: $ACTION" ;;
esac
