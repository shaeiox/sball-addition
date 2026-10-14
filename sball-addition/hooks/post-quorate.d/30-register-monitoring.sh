#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: register-monitoring
# DESCRIPTION: Register this node in the monitoring system after quorate
# TIMEOUT: 60
# RETRY: 2
# RETRY_BACKOFF: 5
# ON_FAIL: continue
# RUN_ONCE: false
# AFTER: network-check
# TARGET: local
# ENABLED: true
### END SBALL-ADDITION INFO
#
# Sample post-quorate hook: notify an external system that the node is ready.
# Without configuration it only writes to the sball-addition log, so the sample
# is safe to leave enabled.
#
# Configuration (all optional):
#   SBALL_MONITORING_URL   registration endpoint (curl POST)
#   SBALL_MONITORING_TOKEN bearer token

set -uo pipefail

# shellcheck source=/dev/null
source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"

URL="${SBALL_MONITORING_URL:-}"
TOKEN="${SBALL_MONITORING_TOKEN:-}"
HOST="${SBALL_HOSTNAME:-$(hostname -s)}"

payload=$(printf '{"host":"%s","state":"%s","master_id":"%s","is_master":%s,"version":"%s"}' \
    "$(_sball_json_escape "$HOST")" \
    "$(_sball_json_escape "${SBALL_QUORATE_STATE:-unknown}")" \
    "${SBALL_MASTER_ID:-0}" \
    "${SBALL_IS_MASTER:-false}" \
    "${SBALL_ADDITION_VERSION:-unknown}")

if [[ -z "$URL" ]]; then
    sball_info "SBALL_MONITORING_URL is not set, payload would be: ${payload}"
    exit 0
fi

if ! sball_have curl; then
    sball_error "curl is required to register with ${URL}"
    exit 1
fi

args=(-sS -m 30 -X POST -H 'Content-Type: application/json' --data "$payload")
[[ -n "$TOKEN" ]] && args+=(-H "Authorization: Bearer ${TOKEN}")

if sball_retry 3 5 curl "${args[@]}" "$URL"; then
    sball_info "registered ${HOST} with ${URL}"
    exit 0
fi

sball_error "failed to register ${HOST} with ${URL}"
exit 1
