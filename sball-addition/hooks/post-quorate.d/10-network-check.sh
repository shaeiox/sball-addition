#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: network-check
# DESCRIPTION: Verify cluster connectivity after the node joined the cluster
# TIMEOUT: 30
# RETRY: 2
# RETRY_BACKOFF: 5
# ON_FAIL: continue
# RUN_ONCE: false
# TARGET: local
# ENABLED: true
### END SBALL-ADDITION INFO
#
# Sample post-quorate hook: runs on every node right after quorate and proves
# that the node can talk to the master.
#
# Configuration (all optional):
#   SBALL_MASTER_HOST   hostname/IP of the master (defaults to SBALL_MASTER_ID)
#   SBALL_CHECK_PORT    TCP port to probe          (default 7090)

set -uo pipefail

# shellcheck source=/dev/null
source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"

MASTER_ID="${SBALL_MASTER_ID:-0}"
MASTER_HOST="${SBALL_MASTER_HOST:-}"
CHECK_PORT="${SBALL_CHECK_PORT:-7090}"

sball_info "quorate=${SBALL_QUORATE_STATE} master_id=${MASTER_ID} is_master=${SBALL_IS_MASTER:-false}"

if [[ -z "$MASTER_HOST" ]]; then
    sball_warn "SBALL_MASTER_HOST is not set, only the local socket is checked"
    MASTER_HOST="127.0.0.1"
fi

if sball_have nc; then
    probe() { nc -z -w 3 "$MASTER_HOST" "$CHECK_PORT"; }
    if sball_retry 3 5 probe; then
        sball_info "master ${MASTER_HOST}:${CHECK_PORT} is reachable"
    else
        sball_error "master ${MASTER_HOST}:${CHECK_PORT} is not reachable"
        exit 1
    fi
else
    sball_warn "nc is not installed, skipping the TCP probe"
fi

if sball_have ping && [[ "$MASTER_HOST" != "127.0.0.1" ]]; then
    if ping -c 1 -W 2 "$MASTER_HOST" >/dev/null 2>&1; then
        sball_info "ping ${MASTER_HOST} ok"
    else
        sball_warn "ping ${MASTER_HOST} failed (ICMP may simply be filtered)"
    fi
fi

sball_info "network check finished"
exit 0
