#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: pre-quorate
# NAME: resource-check
# DESCRIPTION: Verify node prerequisites (paths, tools, free disk) before start
# TIMEOUT: 30
# RETRY: 0
# RETRY_BACKOFF: 2
# ON_FAIL: abort
# RUN_ONCE: false
# TARGET: local
# ENABLED: true
### END SBALL-ADDITION INFO
#
# Sample pre-quorate hook that demonstrates:
#   * reading the injected environment,
#   * logging through lib/common.sh,
#   * failing the service start on purpose (ON_FAIL: abort).
#
# Configuration (all optional):
#   SBALL_REQUIRED_PATHS   colon separated list of paths  (default /etc/sball)
#   SBALL_REQUIRED_TOOLS   space separated list of tools  (default /usr/sbin/sballd)
#   SBALL_MIN_FREE_MB      minimum free MB on /var          (default 0 = off)

set -uo pipefail

# shellcheck source=/dev/null
source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"

REQUIRED_PATHS="${SBALL_REQUIRED_PATHS:-/etc/sball}"
REQUIRED_TOOLS="${SBALL_REQUIRED_TOOLS:-/usr/sbin/sballd}"
MIN_FREE_MB="${SBALL_MIN_FREE_MB:-0}"

rc=0

IFS=':' read -r -a paths <<<"$REQUIRED_PATHS"
for p in "${paths[@]}"; do
    [[ -z "$p" ]] && continue
    if [[ -e "$p" ]]; then
        sball_info "required path present: ${p}"
    else
        sball_error "required path missing: ${p}"
        rc=1
    fi
done

# shellcheck disable=SC2086  # the list is intentionally word split
for tool in $REQUIRED_TOOLS; do
    if [[ -x "$tool" ]]; then
        sball_info "found ${tool}"
    else
        sball_error "not executable: ${tool}"
        rc=1
    fi
done

if ((MIN_FREE_MB > 0)); then
    free_mb=$(df -Pm /var 2>/dev/null | awk 'NR==2 {print $4}')
    if [[ -n "$free_mb" ]] && ((free_mb < MIN_FREE_MB)); then
        sball_error "only ${free_mb}MB free on /var, at least ${MIN_FREE_MB}MB required"
        rc=1
    else
        sball_info "free space on /var: ${free_mb:-unknown}MB"
    fi
fi

if ((rc != 0)); then
    sball_error "pre-quorate checks failed, refusing to start sballd (ON_FAIL=abort)"
fi
exit "$rc"
