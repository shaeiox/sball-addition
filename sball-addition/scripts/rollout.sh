#!/bin/bash
#
# rollout.sh - cluster rollout of sball-addition, one node at a time.
#
# Rolling a virtualisation cluster is mostly about quorum: have sballd down on
# too many nodes at once and the cluster loses quorum and the VMs pay for it. This
# script therefore rolls one node at a time and never takes the service down
# itself:
#
#   1. installs the artifact (optional),
#   2. validates the hook configuration on the node,
#   3. waits until the node reports quorate again,
#   4. verifies the installation (sball-node-verify),
#
# and only then moves to the next node. Non-master nodes are rolled out first,
# the master last, so the cluster always keeps its coordinator as long as the
# remaining nodes are quorate.
#
# sballd.service is never started, stopped, restarted or reconfigured by this
# tool. The service lifecycle stays with the operator: the rollout installs the
# files, validates the hook set, waits for the node to be quorate again and
# verifies it. Nothing else.
#
#   ./scripts/rollout.sh --list --nodes=nodes.txt
#   ./scripts/rollout.sh --nodes=nodes.txt --rpm=release/sball-addition-1.0.0-1.x86_64.rpm
#   ./scripts/rollout.sh --nodes=node1,node2,node3 --tarball=/root/sball-addition-1.0.0.tar.gz
#   ./scripts/rollout.sh --local                       # rehearse on this host
#
# Always run it with --dry-run first: it prints the exact order and commands.
set -uo pipefail

NODES_SPEC=""
SSH_USER=${SBALL_SSH_USER:-root}
SSH_PORT=${SBALL_SSH_PORT:-22}
SSH_OPTS_EXTRA=${SBALL_SSH_OPTS:-}
RPM_FILE=""
TARBALL_FILE=""
WAIT_QUORATE=300
MASTER_LAST=1
RUN_VERIFY=1
CONTINUE_ON_ERROR=0
DRY_RUN=0
LIST_ONLY=0
LOCAL=0
VERBOSE=0
SMOKE=0
MAINTENANCE=""

usage() {
    cat <<'EOF'
usage: rollout.sh --nodes=FILE|host1,host2 [options]
       rollout.sh --local [options]

target selection:
  --nodes=SPEC      file with one host per line (comments allowed) or a comma
                    separated list
  --local           roll out on this machine only (no ssh)
  --list            print the resolved order and exit (combine with --nodes)

artifact (optional - skip to only restart/verify):
  --rpm=FILE        copy this RPM and "rpm -Uvh" it on every node
  --tarball=FILE    copy this tarball, extract it and run its scripts/install.sh

behaviour:
  --wait-quorate=N  seconds to wait for the node to be quorate again (default 300).
                    This only observes the node: the restart itself is yours.
  --no-master-last  keep the given order exactly (default: master node last)
  --no-verify       skip sball-node-verify at the end of each node
  --smoke           pass --smoke to sball-node-verify (runs a real hook)
  --continue-on-error  keep going after a node fails
  --maintenance="TEXT"  reason shown in the log lines (change ticket id)
  --ssh-user=USER   ssh user (default root, $SBALL_SSH_USER)
  --ssh-port=N      ssh port (default 22, $SBALL_SSH_PORT)
  --ssh-opt="OPTS"  extra ssh/scp options ($SBALL_SSH_OPTS)
  --dry-run         print the plan and the commands, run nothing
  -v, --verbose     show the output of every remote command
  -h, --help        this help

examples:
  # rehearse: which order would be used, what would run?
  ./scripts/rollout.sh --nodes=nodes.txt --rpm=release/*.rpm --dry-run

  # production rollout
  sudo ./scripts/rollout.sh --nodes=nodes.txt \
       --rpm=release/sball-addition-1.0.0-1.x86_64.rpm \
       --maintenance="CHG-12345" --smoke
EOF
}

for arg in "$@"; do
    case "$arg" in
        --nodes=*) NODES_SPEC="${arg#*=}" ;;
        --local) LOCAL=1 ;;
        --list) LIST_ONLY=1 ;;
        --rpm=*) RPM_FILE="${arg#*=}" ;;
        --tarball=*) TARBALL_FILE="${arg#*=}" ;;
        --wait-quorate=*) WAIT_QUORATE="${arg#*=}" ;;
        --no-master-last) MASTER_LAST=0 ;;
        --no-verify) RUN_VERIFY=0 ;;
        --smoke) SMOKE=1 ;;
        --continue-on-error) CONTINUE_ON_ERROR=1 ;;
        --maintenance=*) MAINTENANCE="${arg#*=}" ;;
        --ssh-user=*) SSH_USER="${arg#*=}" ;;
        --ssh-port=*) SSH_PORT="${arg#*=}" ;;
        --ssh-opt=*) SSH_OPTS_EXTRA="${arg#*=}" ;;
        --dry-run) DRY_RUN=1 ;;
        -v | --verbose) VERBOSE=1 ;;
        -h | --help) usage; exit 0 ;;
        *)
            echo "rollout.sh: unknown option: $arg" >&2
            usage >&2
            exit 2
            ;;
    esac
done

log() { printf '%s\n' "$*"; }
step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }
warn() { printf '   \033[33mwarning\033[0m %s\n' "$*" >&2; }
err() { printf '   \033[31merror\033[0m %s\n' "$*" >&2; }
good() { printf '   \033[32m%s\033[0m\n' "$*"; }

# ------------------------------------------------------------- validation ----
if ((LOCAL == 0)) && [[ -z "$NODES_SPEC" ]]; then
    echo "rollout.sh: --nodes=FILE|host1,host2 or --local is required" >&2
    exit 2
fi
if [[ -n "$RPM_FILE" && -n "$TARBALL_FILE" ]]; then
    echo "rollout.sh: --rpm and --tarball are mutually exclusive" >&2
    exit 2
fi
if [[ -n "$RPM_FILE" && ! -f "$RPM_FILE" ]]; then
    echo "rollout.sh: RPM '$RPM_FILE' not found" >&2
    exit 2
fi
if [[ -n "$TARBALL_FILE" && ! -f "$TARBALL_FILE" ]]; then
    echo "rollout.sh: tarball '$TARBALL_FILE' not found" >&2
    exit 2
fi
[[ "$WAIT_QUORATE" =~ ^[0-9]+$ ]] || { echo "rollout.sh: --wait-quorate must be a number" >&2; exit 2; }

# --------------------------------------------------------------- node list ---
read_nodes() {
    if [[ -n "$NODES_SPEC" && -f "$NODES_SPEC" ]]; then
        # file: one host per line, '#' comments, any whitespace separated
        sed 's/#.*//' "$NODES_SPEC" | tr -s '[:space:]' '\n' | sed '/^$/d'
    elif [[ -n "$NODES_SPEC" ]]; then
        # comma separated list
        printf '%s' "$NODES_SPEC" | tr ',' '\n' | tr -d '[:blank:]' | sed '/^$/d'
    fi
}

# Note: the "|| [[ -n $node ]]" guard matters - the last line of a command
# substitution has no trailing newline and would otherwise be dropped.
NODES=()
while IFS= read -r node || [[ -n "$node" ]]; do
    node="${node// /}"
    [[ -n "$node" ]] && NODES+=("$node")
done < <(read_nodes)

if ((LOCAL)); then
    NODES=("$(hostname -s 2>/dev/null || hostname)")
    SSH_USER=""
fi
if ((${#NODES[@]} == 0)); then
    echo "rollout.sh: no nodes to roll out" >&2
    exit 2
fi

# ------------------------------------------------------------- ssh helpers ---
# LAST_OUT holds the output of the most recent rsh call (set -u safe)
LAST_OUT=""

SSH_ARGS=(-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new -p "$SSH_PORT")
[[ -n "$SSH_OPTS_EXTRA" ]] && read -r -a __extra <<<"$SSH_OPTS_EXTRA" && SSH_ARGS+=("${__extra[@]}")

# rsh <node> <command...> - run a command on a node (or locally)
rsh() {
    local node=$1
    shift
    if ((LOCAL)); then
        if ((DRY_RUN)); then
            log "      [local] $*"
            LAST_OUT=""
            return 0
        fi
        local local_out
        local_out=$(env SBALL_ADDITION_ROOT="${SBALL_ADDITION_ROOT:-/}" bash -c "$*" 2>&1)
        local local_rc=$?
        ((VERBOSE)) && [[ -n "$local_out" ]] && printf '%s\n' "$local_out" | sed 's/^/      | /'
        LAST_OUT=$local_out
        return $local_rc
    fi
    if ((DRY_RUN)); then
        log "      [$node] ssh $SSH_USER@$node $*"
        LAST_OUT=""
        return 0
    fi
    local out rc
    out=$(ssh "${SSH_ARGS[@]}" "$SSH_USER@$node" "$@" 2>&1)
    rc=$?
    ((VERBOSE)) && [[ -n "$out" ]] && printf '%s\n' "$out" | sed 's/^/      | /'
    LAST_OUT=$out
    return $rc
}

# rcp <node> <local> <remote> - copy a file to a node
rcp() {
    local node=$1 local_file=$2 remote_file=$3
    if ((LOCAL)); then
        ((DRY_RUN)) && { log "      [local] cp $local_file $remote_file"; return 0; }
        cp "$local_file" "$remote_file"
        return $?
    fi
    if ((DRY_RUN)); then
        log "      [$node] scp $local_file $SSH_USER@$node:$remote_file"
        return 0
    fi
    scp -q -o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
        -P "$SSH_PORT" ${SSH_OPTS_EXTRA:+$SSH_OPTS_EXTRA} "$local_file" "$SSH_USER@$node:$remote_file"
}

# is_master <node> - true when the node reports itself as the cluster master
is_master() {
    local node=$1 out
    if ((DRY_RUN || LOCAL)); then
        # Without ssh we cannot know; treat everything as non-master so the
        # printed plan still shows a usable order.
        return 1
    fi
    out=$(ssh "${SSH_ARGS[@]}" "$SSH_USER@$node" "sball-addition status 2>/dev/null | grep -c '\[this node\]'" 2>/dev/null)
    [[ "${out//[^0-9]/}" == "1" ]]
}

# ---------------------------------------------------------------- ordering ---
ORDER=()
MASTER_NODES=()
if ((MASTER_LAST)) && ((LOCAL == 0)); then
    step "resolving the rollout order (non-master nodes first)"
    for node in "${NODES[@]}"; do
        if is_master "$node"; then
            info "$node: is the cluster master -> rolled out last"
            MASTER_NODES+=("$node")
        else
            info "$node: worker"
            ORDER+=("$node")
        fi
    done
    ORDER+=("${MASTER_NODES[@]:-}")
    # drop a possible empty element from "${arr[@]:-}"
    TMP_ORDER=()
    for node in "${ORDER[@]}"; do [[ -n "$node" ]] && TMP_ORDER+=("$node"); done
    ORDER=("${TMP_ORDER[@]}")
else
    ORDER=("${NODES[@]}")
fi

if ((${#ORDER[@]} == 0)); then
    echo "rollout.sh: no nodes left after ordering" >&2
    exit 2
fi

step "plan"
info "nodes (in order): ${ORDER[*]}"
info "artifact:        ${RPM_FILE:-${TARBALL_FILE:-none (install/verify only)}}"
info "restart sballd:  never (this tool does not touch the service)"
info "wait quorate:    ${WAIT_QUORATE}s per node"
info "verify:          $([[ $RUN_VERIFY == 1 ]] && echo yes || echo no)$([[ $SMOKE == 1 ]] && echo ' (+ smoke test)')"
info "dry run:         $([[ $DRY_RUN == 1 ]] && echo yes || echo no)"
[[ -n "$MAINTENANCE" ]] && info "change:          $MAINTENANCE"

if ((LIST_ONLY)); then
    printf '%s\n' "${ORDER[@]}"
    exit 0
fi

if ((DRY_RUN == 0)); then
    warn "this tool never restarts sballd: it installs the files, validates them,"
    warn "waits for the node to be quorate and verifies it. Restarting the daemon"
    warn "(so that a newly applied wrapper is used) is your call and your moment."
    if [[ -t 0 ]]; then
        read -r -p "   type 'yes' to continue: " answer
        [[ "$answer" == "yes" ]] || {
            echo "rollout.sh: aborted"
            exit 1
        }
    fi
fi

# --------------------------------------------------------------- per node ----
RESULTS=()
FAILED_NODES=()

rollout_node() {
    local node=$1
    step "node $node"

    # 1. pre-flight: is anything installed at all?
    if rsh "$node" "command -v sball-addition >/dev/null 2>&1"; then
        local version
        rsh "$node" "sball-addition version" && version="$LAST_OUT" || version="unknown"
        info "installed: ${version:-unknown}"
    else
        if [[ -z "$RPM_FILE" && -z "$TARBALL_FILE" ]]; then
            err "$node: sball-addition is not installed and no artifact was given"
            return 1
        fi
        info "sball-addition is not installed yet"
    fi

    # 2. install the artifact
    if [[ -n "$RPM_FILE" ]]; then
        local base remote
        base=$(basename "$RPM_FILE")
        remote="/tmp/$base"
        info "copying $base"
        rcp "$node" "$RPM_FILE" "$remote" || {
            err "$node: copy failed"
            return 1
        }
        info "installing the RPM"
        rsh "$node" "rpm -Uvh --replacepkgs '$remote'" || {
            err "$node: rpm -Uvh failed"
            return 1
        }
        rsh "$node" "rm -f '$remote'"
    elif [[ -n "$TARBALL_FILE" ]]; then
        local base dir remote
        base=$(basename "$TARBALL_FILE")
        dir="${base%.tar.gz}"
        remote="/tmp/$base"
        info "copying $base"
        rcp "$node" "$TARBALL_FILE" "$remote" || {
            err "$node: copy failed"
            return 1
        }
        info "extracting and running install.sh"
        rsh "$node" "rm -rf '/tmp/$dir' && tar -xzf '$remote' -C /tmp && '/tmp/$dir/scripts/install.sh' --with-systemd --binary='/tmp/$dir/bin/sball-addition'" || {
            err "$node: installation failed"
            return 1
        }
    fi

    # 3. validate: a broken hook set must be caught here, before anything runs
    info "validating the hook configuration"
    if ! rsh "$node" "sball-addition validate"; then
        err "$node: 'sball-addition validate' failed"
        printf '%s\n' "${LAST_OUT:-}" | sed 's/^/      | /' | head -10
        return 1
    fi

    # 4. the service is NOT restarted: wait for the node to be quorate. A node
    #    whose daemon was never stopped answers immediately; a node the operator
    #    is restarting by hand is followed here.
    local waited=0
    info "waiting for the node to be quorate (max ${WAIT_QUORATE}s, no restart)"
    while ((waited < WAIT_QUORATE)); do
        if rsh "$node" "sball-addition watcher --once"; then
            good "$node is quorate after ${waited}s"
            break
        fi
        sleep 5
        waited=$((waited + 5))
    done
    if ((waited >= WAIT_QUORATE)); then
        err "$node did not report the quorate state within ${WAIT_QUORATE}s"
        err "investigate: ssh $SSH_USER@$node 'sball-addition status; pvm --stat; sball-addition log --level=error | tail'"
        return 1
    fi

    # 5. verify
    if ((RUN_VERIFY)); then
        local verify_cmd="sball-node-verify --quiet"
        if ((SMOKE)); then verify_cmd="sball-node-verify --smoke"; fi
        if rsh "$node" "$verify_cmd"; then
            good "$node verified"
        else
            err "$node failed sball-node-verify"
            printf '%s\n' "${LAST_OUT:-}" | sed 's/^/      | /' | head -10
            return 1
        fi
    fi
    return 0
}

STARTED=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ABORTED=0
for node in "${ORDER[@]}"; do
    if rollout_node "$node"; then
        RESULTS+=("$node ok")
    else
        RESULTS+=("$node FAILED")
        FAILED_NODES+=("$node")
        if ((CONTINUE_ON_ERROR == 0)); then
            err "stopping the rollout (use --continue-on-error to continue)"
            ABORTED=1
            break
        fi
    fi
done

step "summary"
if ((DRY_RUN)); then
    info "dry run: nothing was executed, no node was touched"
fi
info "started: $STARTED, finished: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
for line in "${RESULTS[@]}"; do
    case "$line" in
        *FAILED) err "$line" ;;
        *) good "$line" ;;
    esac
done

if ((ABORTED)); then
    err "rollout incomplete: the remaining nodes were not touched"
fi
if ((${#FAILED_NODES[@]} > 0)); then
    err "failed nodes: ${FAILED_NODES[*]}"
    exit 1
fi

if ((DRY_RUN)); then
    good "dry run finished: ${#ORDER[@]} node(s) would be rolled out"
else
    good "rollout finished: ${#ORDER[@]} node(s)"
fi
if ((${#NODES[@]} != ${#ORDER[@]})); then
    warn "$(( ${#NODES[@]} - ${#ORDER[@]} )) node(s) were skipped"
fi
exit 0
