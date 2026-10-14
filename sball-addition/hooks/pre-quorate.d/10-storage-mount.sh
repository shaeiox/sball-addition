#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: pre-quorate
# NAME: storage-mount
# DESCRIPTION: Make sure the VM storage pool is mounted before sballd starts
# TIMEOUT: 60
# RETRY: 2
# RETRY_BACKOFF: 5
# ON_FAIL: continue
# RUN_ONCE: false
# TARGET: local
# ENABLED: true
### END SBALL-ADDITION INFO
#
# Sample pre-quorate hook.
#
# Copy it, adjust the variables below and flip ON_FAIL to abort when a missing
# storage pool must really block the service (see the README, "Sample hooks").
#
# Configuration (all optional):
#   SBALL_STORAGE_MOUNT   mount point to check   (default /var/lib/pvm-storage)
#   SBALL_STORAGE_DEVICE  device to mount, when the pool is not mounted yet

set -uo pipefail

# shellcheck source=/dev/null
source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"

STORAGE_MOUNT="${SBALL_STORAGE_MOUNT:-/var/lib/pvm-storage}"
STORAGE_DEVICE="${SBALL_STORAGE_DEVICE:-}"

sball_info "phase=${SBALL_HOOK_PHASE} host=${SBALL_HOSTNAME} mount=${STORAGE_MOUNT}"

if [[ ! -d "$STORAGE_MOUNT" ]]; then
    mkdir -p "$STORAGE_MOUNT" 2>/dev/null || true
fi

if sball_mounted "$STORAGE_MOUNT"; then
    sball_info "storage already mounted at ${STORAGE_MOUNT}"
    exit 0
fi

if [[ -z "$STORAGE_DEVICE" ]]; then
    sball_warn "nothing mounted at ${STORAGE_MOUNT} and SBALL_STORAGE_DEVICE is unset (nothing to do)"
    exit 0
fi

if [[ ! -b "$STORAGE_DEVICE" ]]; then
    sball_error "storage device ${STORAGE_DEVICE} is not a block device"
    exit 1
fi

sball_require mount || exit 1
if ! sball_run "mount ${STORAGE_DEVICE} ${STORAGE_MOUNT}" mount "$STORAGE_DEVICE" "$STORAGE_MOUNT"; then
    exit 1
fi

exit 0
