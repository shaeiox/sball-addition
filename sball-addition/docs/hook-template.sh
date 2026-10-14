#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: my-hook
# DESCRIPTION: One line describing what this hook does
# TIMEOUT: 60
# RETRY: 2
# RETRY_BACKOFF: 5
# ON_FAIL: continue
# RUN_ONCE: false
# AFTER: some-other-hook
# TARGET: local
# ENABLED: true
### END SBALL-ADDITION INFO
#
# Copy this file into /etc/sball-addition/hooks.d/<phase>.d/ and adjust it:
#
#   install -m 0755 my-hook.sh /etc/sball-addition/hooks.d/post-quorate.d/40-my-hook.sh
#   sball-addition install-hook ./my-hook.sh --phase=post-quorate --order=40
#   sball-addition validate && sball-addition list
#
# Header fields (only HOOK and NAME are mandatory):
#   HOOK           pre-quorate | post-quorate
#   NAME           unique identifier inside the phase
#   TIMEOUT        seconds before SIGTERM/SIGKILL        (default: general.default_timeout)
#   RETRY          extra attempts after the first one    (default 0)
#   RETRY_BACKOFF  seconds between attempts              (default 5)
#   ON_FAIL        abort | continue                      (default: phase policy)
#   RUN_ONCE       true | false, per server lifetime     (default false)
#   AFTER          name of a hook that must run first
#   TARGET         local | master | all                  (default local)
#   ENABLED        true | false                          (default true)
#
# Values may use ${VAR} or ${VAR:-default} placeholders, which are expanded from
# the environment when the hook is parsed - handy for cluster specific values.
#
# Exit 0 for success, anything else for failure. stdout/stderr are captured and
# stored in the JSON log of the failing run.

set -euo pipefail

# shellcheck source=/dev/null
source "${SBALL_COMMON_LIB:-/etc/sball-addition/lib/common.sh}"

sball_info "starting ${SBALL_HOOK_NAME} on ${SBALL_HOSTNAME} (phase=${SBALL_HOOK_PHASE}, attempt=${SBALL_RETRY_ATTEMPT:-1})"
sball_debug "master_id=${SBALL_MASTER_ID:-0} is_master=${SBALL_IS_MASTER:-false} state=${SBALL_QUORATE_STATE:-unknown}"

# ... do the actual work here ...
#
# Examples:
#   sball_require curl jq || exit 1
#   sball_run "reload the agent" systemctl reload my-agent
#   sball_retry 3 5 my-command --flag
#   sball_mark_state            # write $SBALL_STATE_FILE when RUN_ONCE is not enough

if ! true; then
    sball_error "the work failed"
    exit 1
fi

sball_info "finished ${SBALL_HOOK_NAME}"
exit 0
