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

The package is self contained: it needs no external service and no changes to
the PVM installation beyond pointing sballd.service at the wrapper (a systemd
drop-in is shipped, but not enabled automatically).

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
install -d %{buildroot}%{_sysconfdir}/systemd/system/sballd.service.d
install -m 0644 packaging/systemd/sballd.service.d/sball-addition.conf \
    %{buildroot}%{_sysconfdir}/systemd/system/sballd.service.d/sball-addition.conf

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

  2. run the pre/post hooks with the service (systemd drop-in is in place):
       systemctl daemon-reload && systemctl restart sballd.service

  3. keep the post-quorate watcher alive across sballd restarts:
       systemctl enable --now sball-addition-watcher.service

  4. starts that bypass systemd (pvm-wrapper --start) can be covered too:
       ./scripts/install.sh --shim-sballd

  logs: /var/log/sball-addition/sball-addition.log (JSON, jq friendly)

EOF

%preun
if [ $1 -eq 0 ]; then
    systemctl stop sball-addition-watcher.service >/dev/null 2>&1 || :
    systemctl disable sball-addition-watcher.service >/dev/null 2>&1 || :
    # Remove the ExecStart override so sballd starts the plain daemon again.
    rm -f %{_sysconfdir}/systemd/system/sballd.service.d/sball-addition.conf
    rmdir %{_sysconfdir}/systemd/system/sballd.service.d 2>/dev/null || :
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
%config(noreplace) %{_sysconfdir}/systemd/system/sballd.service.d/sball-addition.conf
%{_unitdir}/sball-addition-watcher.service
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
