#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: sync-config
# DESCRIPTION: Sync the shared configuration across the cluster (master only)
# TIMEOUT: 120
# RETRY: 1
# RETRY_BACKOFF: 10
# ON_FAIL: continue
# RUN_ONCE: true
# AFTER: network-check
# TARGET: master
# ENABLED: true
### END SBALL-ADDITION INFO
#
# Sample post-quorate hook that demonstrates TARGET=master, RUN_ONCE and AFTER.
# It runs only on the cluster master, and only once per server lifetime
# (the marker lives in /var/lib/sball-addition/state/post-quorate/).
#
# Configuration (all optional):
#   SBALL_SYNC_SOURCE   source directory to publish (default /etc/sball)
#   SBALL_SYNC_TARGET   destination path          (default /var/lib/pvm-sync)

set -uo pipefail

# shellcheck source=/dev/null
source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"

if ! sball_is_master; then
    # The runner already skips this hook on non-master nodes; this is a belt
    # and braces check for manual invocations (sball-addition run sync-config).
    sball_warn "this node is not the master (master_id=${SBALL_MASTER_ID:-0}), nothing to do"
    exit 0
fi

SOURCE_DIR="${SBALL_SYNC_SOURCE:-/etc/sball}"
TARGET_DIR="${SBALL_SYNC_TARGET:-/var/lib/pvm-sync}"

if [[ ! -d "$SOURCE_DIR" ]]; then
    sball_warn "source directory ${SOURCE_DIR} does not exist, nothing to sync"
    exit 0
fi

mkdir -p "$TARGET_DIR"

sball_require cp || exit 1
if sball_run "publish ${SOURCE_DIR} to ${TARGET_DIR}" \
    cp -a "${SOURCE_DIR}/." "$TARGET_DIR/"; then
    sball_info "configuration published to ${TARGET_DIR}"
    exit 0
fi

sball_error "publishing ${SOURCE_DIR} failed"
exit 1
