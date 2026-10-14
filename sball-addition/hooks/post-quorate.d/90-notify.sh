#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: notify
# DESCRIPTION: Broadcast the quorate event to every node (TARGET=all)
# TIMEOUT: 30
# RETRY: 1
# RETRY_BACKOFF: 5
# ON_FAIL: continue
# RUN_ONCE: false
# AFTER: register-monitoring
# TARGET: all
# ENABLED: true
### END SBALL-ADDITION INFO
#
# Sample hook showing TARGET=all and AFTER ordering.
#
# NOTE (MVP): TARGET=all does not coordinate across nodes - every node runs its
# own copy of the hook, sequentially after the hook listed in AFTER. Use it for
# idempotent, node local actions (log shippers, agent reloads, ...).
#
# Configuration (all optional):
#   SBALL_NOTIFY_URL   webhook to POST to
#   SBALL_NOTIFY_TAG   extra tag for the message

set -uo pipefail

# shellcheck source=/dev/null
source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"

URL="${SBALL_NOTIFY_URL:-}"
TAG="${SBALL_NOTIFY_TAG:-pvm}"

message="${TAG}: ${SBALL_HOSTNAME:-$(hostname -s)} is ${SBALL_QUORATE_STATE:-unknown} (master_id=${SBALL_MASTER_ID:-0}, run ${SBALL_RUN_ID:-unknown})"

if [[ -z "$URL" ]]; then
    sball_info "notify: ${message}"
    exit 0
fi

if ! sball_have curl; then
    sball_warn "curl is not installed, cannot notify ${URL}"
    exit 0
fi

if curl -sS -m 15 -X POST -H 'Content-Type: text/plain' --data "$message" "$URL"; then
    sball_info "notification sent to ${URL}"
else
    sball_warn "notification to ${URL} failed"
fi

exit 0
