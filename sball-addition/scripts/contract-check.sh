#!/bin/bash
#
# contract-check.sh - enforce the rule that sballd.service is never touched.
#
# The rule: no installer, package, rollout, unit or daemon of sball-addition
# ever starts, stops, restarts, enables, disables or reconfigures the PVM
# service, and none of them writes a unit file, drop-in or binary that belongs
# to it. The only exception is sball-integration, which is run by hand, writes
# the drop-in (or the shim) and *prints* the systemctl line instead of running
# it. uninstall.sh is allowed to print that line too: it removes the drop-in it
# owns and tells the operator what to do next.
#
# This is the static half of the enforcement. test/integration/run.sh adds the
# behavioural half: every program is re-run with a fake systemctl first in PATH
# and the recorded calls must never mention sballd.
#
# Run it directly or through "make contract-check" (part of "make test").
set -uo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
FAIL=0

pass() { printf '  ok   %s\n' "$*"; }
fail() {
    printf '  FAIL %s\n' "$*"
    FAIL=$((FAIL + 1))
}
head_lines() { head -2 | tr '\n' ' ' | sed 's/[[:space:]]*$//'; }

# code_lines <file> [go]
#
# Prints only the lines a shell (or Go) interpreter could act on: comments are
# dropped, and so are the bodies of heredocs, which are messages for humans.
# Lines that are obviously messages (printf/echo/info/warn/ok/fail/... with a
# string) are dropped as well: telling an operator which command to run is the
# whole point of this design, executing it is what must not happen.
code_lines() {
    local file=$1 kind=${2:-shell}
    if [[ "$kind" == "go" ]]; then
        grep -vE '^[[:space:]]*(//|/\*|\*)' "$file" 2>/dev/null || true
        return
    fi
    awk '
        BEGIN { heredoc = "" }
        {
            line = $0
            if (heredoc != "") {
                if (line ~ ("^[[:space:]]*" heredoc "[[:space:]]*$")) { heredoc = "" }
                next
            }
            if (line ~ /^[[:space:]]*#/) next
            if (line ~ /<<-?[[:space:]]*[\x27"]?[A-Za-z_][A-Za-z0-9_]*[\x27"]?/) {
                match(line, /<<-?[[:space:]]*[\x27"]?[A-Za-z_][A-Za-z0-9_]*[\x27"]?/)
                tok = substr(line, RSTART, RLENGTH)
                gsub(/<<-?[[:space:]]*/, "", tok)
                gsub(/[\x27"]/, "", tok)
                heredoc = tok
            }
            print line
        }
    ' "$file" 2>/dev/null |
        grep -vE '^[[:space:]]*(printf|echo|info|warn|note|log|cat|ok|fail|usage|msg|die|err)[[:space:]]' || true
}

# ---------------------------------------------------------------- 1. Go code --
# The application itself: it may read the process table and poll the PVM socket,
# but it must not know how to control the service.
printf '\n== the application (Go) cannot control systemd or name the unit\n'
GO_BAD=0
while IFS= read -r f; do
    hits=$(code_lines "$f" go | grep -nE 'systemctl|sballd\.service' || true)
    if [[ -n "$hits" ]]; then
        fail "${f#"$ROOT"/}: $(printf '%s' "$hits" | head_lines)"
        GO_BAD=1
    fi
done < <(find "$ROOT/internal" "$ROOT/cmd" -name '*.go' -type f 2>/dev/null | sort)
((GO_BAD)) || pass "internal/ and cmd/: no systemctl, no sballd.service"

# ------------------------------------------------------- 2. scripts + deploy --
printf '\n== scripts never issue a service command\n'
# systemctl <verb> ... sballd, or sballd.service <verb>, outside the allowlist
CONTROL='systemctl[^|;]*[[:space:]](start|stop|restart|reload|try-restart|enable|disable|mask|kill)[^|;]*sballd|sballd([^|;]*\.service)?[^|;]*\.(service)?[^|;]*(restart|start|stop)[^|;]*systemctl'
CONTROL2='(^|[[:space:];&|])(systemctl[[:space:]]+(restart|stop|start|reload|enable|disable)[[:space:]]+sballd|sballd\.service[[:space:]]+(restart|stop|start))'
SCRIPT_BAD=0
is_allowed() {
    case ${1#"$ROOT"/} in
        scripts/sballd-integration.sh | scripts/uninstall.sh | packaging/debian/postinst | packaging/debian/postrm | packaging/sball-addition.spec | scripts/contract-check.sh) return 0 ;;
        *) return 1 ;;
    esac
}
while IFS= read -r f; do
    is_allowed "$f" && continue
    hits=$(code_lines "$f" | grep -nE "$CONTROL|$CONTROL2" || true)
    if [[ -n "$hits" ]]; then
        fail "${f#"$ROOT"/}: $(printf '%s' "$hits" | head_lines)"
        SCRIPT_BAD=1
    fi
done < <(find "$ROOT/scripts" "$ROOT/lib" "$ROOT/hooks" "$ROOT/deploy" "$ROOT/packaging/debian" \
    -type f \( -name '*.sh' -o -name 'postinst' -o -name 'prerm' -o -name 'postrm' \
    -o -name '*.yml' \) 2>/dev/null | sort)
hits=$(code_lines "$ROOT/packaging/sball-addition.spec" | grep -nE "$CONTROL|$CONTROL2" || true)
if [[ -n "$hits" ]]; then
    # %preun removes the drop-in we own; it must not drive systemctl (the
    # printed instruction lives in a heredoc and is already filtered out).
    fail "packaging/sball-addition.spec: $(printf '%s' "$hits" | head_lines)"
    SCRIPT_BAD=1
fi
((SCRIPT_BAD)) || pass "scripts/, lib/, hooks/, deploy/, packaging/: nothing controls sballd"

# ------------------------------------------------------------------ 3. units --
printf '\n== our units have no dependency on sballd.service\n'
UNIT_BAD=0
for f in "$ROOT"/packaging/systemd/sball-addition-*.service "$ROOT"/packaging/systemd/sball-addition-*.timer; do
    [[ -f "$f" ]] || continue
    hits=$(code_lines "$f" | grep -nE '^(After|Before|Wants|Requires|Requisite|BindsTo|PartOf|Conflicts|PropagatesReloadTo)=.*sballd' || true)
    hits+=$(code_lines "$f" | grep -nE '^(ExecStart|ExecStop|ExecReload)=.*(systemctl|sballd\.service)' || true)
    if [[ -n "$hits" ]]; then
        fail "${f#"$ROOT"/}: $(printf '%s' "$hits" | head_lines)"
        UNIT_BAD=1
    fi
done
((UNIT_BAD)) || pass "watcher and metrics units: no sballd dependency, no systemctl"

# ------------------------------------------- 4. nobody writes a daemon file ---
printf '\n== only the hand-run integration tool writes a sballd file\n'
WRITER_BAD=0
for f in "$ROOT/scripts/install.sh" "$ROOT/scripts/rollout.sh" \
    "$ROOT/packaging/debian/build-deb.sh" "$ROOT/packaging/debian/postinst"; do
    [[ -f "$f" ]] || continue
    if code_lines "$f" | grep -qE 'sballd\.service\.d'; then
        fail "${f#"$ROOT"/} references /etc/systemd/system/sballd.service.d"
        WRITER_BAD=1
    fi
done
if code_lines "$ROOT/packaging/sball-addition.spec" | grep -qE 'install[^|]*sballd\.service\.d'; then
    fail "packaging/sball-addition.spec installs a drop-in for sballd.service"
    WRITER_BAD=1
fi
# verify-node.sh is allowed to *read* the drop-in (that is a check, not a
# change); it may not write, move or delete it.
if code_lines "$ROOT/scripts/verify-node.sh" | grep -qE '(install|rm|mv|cp|mkdir|touch|tee|chmod|chown)[^|]*sballd\.service\.d'; then
    fail "scripts/verify-node.sh writes or moves the sballd drop-in (it may only read it)"
    WRITER_BAD=1
fi
((WRITER_BAD)) || pass "install.sh, rollout.sh, the DEB and the RPM install no sballd file"

printf '\n== the one writer is run by hand\n'
if [[ -f "$ROOT/scripts/sballd-integration.sh" ]]; then
    pass "scripts/sballd-integration.sh exists (the documented, hand-run exception)"
else
    fail "scripts/sballd-integration.sh is missing"
fi
CALL_BAD=0
for f in "$ROOT/scripts/install.sh" "$ROOT/scripts/rollout.sh" "$ROOT/scripts/verify-node.sh" \
    "$ROOT/scripts/uninstall.sh" "$ROOT/packaging/debian/postinst" "$ROOT/packaging/debian/postrm" \
    "$ROOT/ansible"/* "$ROOT/deploy/ansible"/*.yml "$ROOT"/packaging/systemd/sball-addition-*.service \
    "$ROOT/scripts/contract-check.sh"; do
    [[ -f "$f" ]] || continue
    if code_lines "$f" | grep -qE '(^|[[:space:]/])(sball-integration|sballd-integration\.sh)[[:space:]]+(status|apply|remove|--print)'; then
        fail "${f#"$ROOT"/} calls the integration tool (it must only be run by hand)"
        CALL_BAD=1
    fi
done
((CALL_BAD)) || pass "no installer, rollout, unit or package script calls it"

printf '\n'
if ((FAIL)); then
    printf 'contract-check: %d violation(s) - the "sballd.service is never touched" rule is broken\n' "$FAIL"
    exit 1
fi
printf 'contract-check: OK - sballd.service is never started, stopped, restarted or reconfigured\n'
