%global debug_package %{nil}
%global _hardened_build 1

# Non Red Hat systems (and older rpm) do not define _unitdir; sballd lives in
# /usr/lib/systemd/system there as well.
%{!?_unitdir:%global _unitdir /usr/lib/systemd/system}

Name:           sball-addition
Version:        %{?version}%{!?version:1.0.0}
Release:        %{?releasenumber}%{!?releasenumber:1}%{?dist}
Summary:        Managed hook system for the sballd (PVM) service
License:        MIT
URL:            https://www.cloudavid.com/
Source0:        %{name}-%{version}.tar.gz
BuildArch:      x86_64
Requires:       bash >= 4.2
Requires:       systemd
Requires(post): systemd
Requires(preun): systemd
Requires(postun): systemd
Conflicts:      sball-hooks

%description
sball-addition runs administrator supplied scripts at two points of the sballd
(PVM) service lifecycle:

  * pre-quorate  - before sballd starts, while the service is still down
  * post-quorate - after the node has joined the cluster (quorate)

It provides a CLI (list, run, status, log, enable/disable, install/uninstall,
validate), a wrapper for the service unit, a quorate watcher, JSON logging,
timeouts, retries, RUN_ONCE markers and cluster aware targets (local, master,
all).

The package is self contained and treats the service as read-only: it never
starts, stops, restarts or reconfigures sballd.service, and it installs no unit
file, drop-in or binary that belongs to it. Hooks run when the daemon is started
through the wrapper, which is an administrator decision:

    sball-integration apply     # write the ExecStart drop-in (then reload/restart)
    sball-integration status    # show whether it is applied

Until that is done everything stays observational: the tool polls the PVM
socket, reports the node state and runs hooks on demand.

%prep
%setup -q

%build
# The tarball ships a prebuilt static binary (built by "make tarball").
if [ ! -x bin/sball-addition ]; then
    echo "bin/sball-addition is missing from the source tarball" >&2
    exit 1
fi

%install
rm -rf %{buildroot}
install -d %{buildroot}%{_bindir} %{buildroot}%{_sbindir}
install -m 0755 bin/sball-addition       %{buildroot}%{_bindir}/sball-addition
install -m 0755 scripts/sballd-wrapper   %{buildroot}%{_bindir}/sballd-wrapper
install -m 0755 scripts/verify-node.sh   %{buildroot}%{_bindir}/sball-node-verify
install -m 0755 scripts/rollout.sh       %{buildroot}%{_bindir}/sball-rollout
install -m 0755 scripts/sballd-integration.sh %{buildroot}%{_bindir}/sball-integration
install -m 0755 scripts/sballd-wrapper   %{buildroot}%{_sbindir}/sballd-wrapper

install -d %{buildroot}%{_sysconfdir}/sball-addition/hooks.d/pre-quorate.d
install -d %{buildroot}%{_sysconfdir}/sball-addition/hooks.d/post-quorate.d
install -d %{buildroot}%{_sysconfdir}/sball-addition/lib
install -m 0644 etc/sball-addition.conf  %{buildroot}%{_sysconfdir}/sball-addition/sball-addition.conf
install -m 0644 lib/common.sh            %{buildroot}%{_sysconfdir}/sball-addition/lib/common.sh
for hook in hooks/pre-quorate.d/*.sh; do
    install -m 0755 "$hook" %{buildroot}%{_sysconfdir}/sball-addition/hooks.d/pre-quorate.d/
done
for hook in hooks/post-quorate.d/*.sh; do
    install -m 0755 "$hook" %{buildroot}%{_sysconfdir}/sball-addition/hooks.d/post-quorate.d/
done

install -d %{buildroot}%{_localstatedir}/lib/sball-addition/state/pre-quorate
install -d %{buildroot}%{_localstatedir}/lib/sball-addition/state/post-quorate
install -d %{buildroot}%{_localstatedir}/lib/sball-addition/runtime
install -d %{buildroot}%{_localstatedir}/log/sball-addition

install -d %{buildroot}%{_unitdir}
install -m 0644 packaging/systemd/sball-addition-watcher.service \
    %{buildroot}%{_unitdir}/sball-addition-watcher.service
install -m 0644 packaging/systemd/sball-addition-metrics.service \
    %{buildroot}%{_unitdir}/sball-addition-metrics.service
install -m 0644 packaging/systemd/sball-addition-metrics.timer \
    %{buildroot}%{_unitdir}/sball-addition-metrics.timer

install -d %{buildroot}%{_mandir}/man8
install -m 0644 docs/sball-addition.8     %{buildroot}%{_mandir}/man8/sball-addition.8
install -m 0644 docs/sballd-wrapper.8     %{buildroot}%{_mandir}/man8/sballd-wrapper.8
install -m 0644 docs/sball-node-verify.8  %{buildroot}%{_mandir}/man8/sball-node-verify.8
install -m 0644 docs/sball-rollout.8      %{buildroot}%{_mandir}/man8/sball-rollout.8
install -m 0644 docs/sball-integration.8  %{buildroot}%{_mandir}/man8/sball-integration.8

install -d %{buildroot}%{_sysconfdir}/logrotate.d
install -m 0644 packaging/logrotate.d/sball-addition \
    %{buildroot}%{_sysconfdir}/logrotate.d/sball-addition
install -d %{buildroot}%{_sysconfdir}/sysconfig
install -m 0644 packaging/sysconfig/sball-addition \
    %{buildroot}%{_sysconfdir}/sysconfig/sball-addition

%post
systemctl daemon-reload >/dev/null 2>&1 || :
cat <<'EOF'

sball-addition installed.

  1. install or write hooks:
       /etc/sball-addition/hooks.d/pre-quorate.d/
       /etc/sball-addition/hooks.d/post-quorate.d/
     verify them with:  sball-addition validate && sball-addition list
     check the node with:  sball-node-verify --smoke

  2. run hooks on demand, without touching the service:
       sball-addition run-phase pre-quorate
       sball-addition run-phase post-quorate

  3. run the hooks with the service (OPTIONAL, your decision - the package does
     not do this for you; it writes no file for sballd.service until you ask):
       sball-integration status
       sball-integration apply      # then: systemctl daemon-reload && systemctl restart sballd.service

  4. keep the post-quorate watcher alive across daemon restarts:
       systemctl enable --now sball-addition-watcher.service

  5. optional Prometheus metrics (node_exporter textfile collector):
       echo 'SBALL_METRICS_ARGS=--out /var/lib/node_exporter/textfile_collector/sball-addition.prom' \
           >> /etc/sysconfig/sball-addition
       systemctl enable --now sball-addition-metrics.timer

  logs: /var/log/sball-addition/sball-addition.log (JSON, jq friendly)
  deploy: PRODUCTION.md (also installed as %{_docdir}/%{name}/PRODUCTION.md)

EOF

%preun
if [ $1 -eq 0 ]; then
    systemctl stop sball-addition-watcher.service >/dev/null 2>&1 || :
    systemctl disable sball-addition-watcher.service >/dev/null 2>&1 || :
    # If (and only if) an administrator applied the ExecStart drop-in, take it
    # away again: leaving it would point the service at a wrapper that the next
    # command removes. sballd.service itself is not started, stopped or
    # restarted here - reloading is the administrator's step.
    if [ -f %{_sysconfdir}/systemd/system/sballd.service.d/sball-addition.conf ]; then
        rm -f %{_sysconfdir}/systemd/system/sballd.service.d/sball-addition.conf
        rmdir %{_sysconfdir}/systemd/system/sballd.service.d 2>/dev/null || :
        cat <<'EON'

The sballd ExecStart drop-in was removed because sball-addition is going away.
sballd.service is still running the wrapper in memory - run
"systemctl daemon-reload && systemctl restart sballd.service" when it suits you;
sball-addition never restarts the daemon itself.

EON
    fi
    systemctl daemon-reload >/dev/null 2>&1 || :
fi

%postun
if [ $1 -eq 0 ]; then
    systemctl daemon-reload >/dev/null 2>&1 || :
    # Runtime state and logs are intentionally left in place; remove them with
    # "scripts/uninstall.sh" or manually, or turn this into an erasure:
    #   rm -rf /var/lib/sball-addition /var/log/sball-addition
    :
fi

%files
%{_bindir}/sball-addition
%{_bindir}/sballd-wrapper
%{_bindir}/sball-node-verify
%{_bindir}/sball-rollout
%{_bindir}/sball-integration
%{_sbindir}/sballd-wrapper
%dir %{_sysconfdir}/sball-addition
%dir %{_sysconfdir}/sball-addition/hooks.d
%dir %{_sysconfdir}/sball-addition/hooks.d/pre-quorate.d
%dir %{_sysconfdir}/sball-addition/hooks.d/post-quorate.d
%config(noreplace) %{_sysconfdir}/sball-addition/sball-addition.conf
%config(noreplace) %{_sysconfdir}/sball-addition/hooks.d/pre-quorate.d/*.sh
%config(noreplace) %{_sysconfdir}/sball-addition/hooks.d/post-quorate.d/*.sh
%dir %{_sysconfdir}/sball-addition/lib
%{_sysconfdir}/sball-addition/lib/common.sh
%config(noreplace) %{_sysconfdir}/sysconfig/sball-addition
%config(noreplace) %{_sysconfdir}/logrotate.d/sball-addition
%{_unitdir}/sball-addition-watcher.service
%{_unitdir}/sball-addition-metrics.service
%{_unitdir}/sball-addition-metrics.timer
%{_mandir}/man8/sball-addition.8*
%{_mandir}/man8/sballd-wrapper.8*
%{_mandir}/man8/sball-node-verify.8*
%{_mandir}/man8/sball-rollout.8*
%{_mandir}/man8/sball-integration.8*
%doc README.md PRODUCTION.md docs/hook-template.sh
%dir %{_localstatedir}/lib/sball-addition
%dir %{_localstatedir}/lib/sball-addition/state
%dir %{_localstatedir}/lib/sball-addition/state/pre-quorate
%dir %{_localstatedir}/lib/sball-addition/state/post-quorate
%dir %{_localstatedir}/lib/sball-addition/runtime
%dir %{_localstatedir}/log/sball-addition

%changelog
* Wed Oct 14 2026 sball-addition maintainers <sball-addition@cloudavid.com> - 1.0.0-1
- Initial release: pre/post-quorate hooks, watcher, CLI, JSON logging,
  timeouts, retries, RUN_ONCE, TARGET, systemd units, sample hooks.
