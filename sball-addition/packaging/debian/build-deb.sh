#!/bin/bash
#
# build-deb.sh - build a Debian/Ubuntu package for sball-addition.
#
# Uses dpkg-deb directly (no debhelper, no dh-make), so it runs on any machine
# with dpkg-deb available - including a RHEL build host.
#
#   ./packaging/debian/build-deb.sh                       # -> release/sball-addition_1.0.0-1_amd64.deb
#   ./packaging/debian/build-deb.sh --backend=tarball     # build from the tarball instead
#   VERSION=1.0.1 ARCH=arm64 ./packaging/debian/build-deb.sh
#
# The resulting package installs the same tree as the RPM:
#   /usr/bin/sball-addition, /usr/bin/sballd-wrapper, /usr/bin/sball-node-verify,
#   /usr/bin/sball-rollout, /usr/sbin/sballd-wrapper,
#   /etc/sball-addition/{sball-addition.conf,lib/common.sh,hooks.d/...},
#   /etc/sysconfig/sball-addition, /etc/logrotate.d/sball-addition,
#   /etc/systemd/system/sballd.service.d/sball-addition.conf,
#   /usr/lib/systemd/system/sball-addition-watcher.service (+ -metrics.{service,timer}),
#   /var/lib/sball-addition/{state,runtime}, /var/log/sball-addition
set -euo pipefail

HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd -- "$HERE/../.." && pwd)

VERSION=${VERSION:-1.0.0}
RELEASE=${RELEASE:-1}
ARCH=${ARCH:-amd64}
BINARY=${BINARY:-$REPO/bin/sball-addition}
DIST=${DIST:-$REPO/release}
MAINTAINER=${MAINTAINER:-CloudAvid PVM team <pvm@cloudavid.com>}
TARBALL=""

for arg in "$@"; do
    case "$arg" in
        --backend=tarball) TARBALL="$DIST/sball-addition-$VERSION.tar.gz" ;;
        --binary=*) BINARY="${arg#*=}" ;;
        --version=*) VERSION="${arg#*=}" ;;
        --arch=*) ARCH="${arg#*=}" ;;
        --dist=*) DIST="${arg#*=}" ;;
        --maintainer=*) MAINTAINER="${arg#*=}" ;;
        -h | --help)
            sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "build-deb.sh: unknown option: $arg" >&2
            exit 2
            ;;
    esac
done

command -v dpkg-deb >/dev/null 2>&1 || {
    echo "build-deb.sh: dpkg-deb is required (apt install dpkg / dnf install dpkg)" >&2
    exit 1
}

# A tarball can ship the prebuilt binary; otherwise build it first.
SRC="$REPO"
if [[ -n "$TARBALL" ]]; then
    [[ -f "$TARBALL" ]] || {
        echo "build-deb.sh: $TARBALL not found, run 'make tarball' first" >&2
        exit 1
    }
    UNPACK=$(mktemp -d)
    trap 'rm -rf "$UNPACK"' EXIT
    tar -xzf "$TARBALL" -C "$UNPACK"
    SRC="$UNPACK/sball-addition-$VERSION"
fi
if [[ ! -x "$SRC/bin/sball-addition" ]]; then
    echo "build-deb.sh: $SRC/bin/sball-addition not found, run 'make build' first" >&2
    exit 1
fi

STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

echo "staging into $STAGE"
install -d "$STAGE/DEBIAN" \
    "$STAGE/usr/bin" "$STAGE/usr/sbin" \
    "$STAGE/usr/lib/systemd/system" \
    "$STAGE/etc/systemd/system/sballd.service.d" \
    "$STAGE/etc/sball-addition/hooks.d/pre-quorate.d" \
    "$STAGE/etc/sball-addition/hooks.d/post-quorate.d" \
    "$STAGE/etc/sball-addition/lib" \
    "$STAGE/etc/sysconfig" "$STAGE/etc/logrotate.d" \
    "$STAGE/var/lib/sball-addition/state/pre-quorate" \
    "$STAGE/var/lib/sball-addition/state/post-quorate" \
    "$STAGE/var/lib/sball-addition/runtime" \
    "$STAGE/var/log/sball-addition" \
    "$STAGE/usr/share/doc/sball-addition"

# ------------------------------------------------------------------ files ---
install -m 0755 "$SRC/bin/sball-addition"           "$STAGE/usr/bin/sball-addition"
install -m 0755 "$SRC/scripts/sballd-wrapper"       "$STAGE/usr/bin/sballd-wrapper"
install -m 0755 "$SRC/scripts/sballd-wrapper"       "$STAGE/usr/sbin/sballd-wrapper"
install -m 0755 "$SRC/scripts/verify-node.sh"       "$STAGE/usr/bin/sball-node-verify"
install -m 0755 "$SRC/scripts/rollout.sh"           "$STAGE/usr/bin/sball-rollout"

install -m 0644 "$SRC/etc/sball-addition.conf"      "$STAGE/etc/sball-addition/sball-addition.conf"
install -m 0644 "$SRC/lib/common.sh"                "$STAGE/etc/sball-addition/lib/common.sh"
for hook in "$SRC"/hooks/pre-quorate.d/*.sh; do
    install -m 0755 "$hook" "$STAGE/etc/sball-addition/hooks.d/pre-quorate.d/"
done
for hook in "$SRC"/hooks/post-quorate.d/*.sh; do
    install -m 0755 "$hook" "$STAGE/etc/sball-addition/hooks.d/post-quorate.d/"
done
install -m 0644 "$SRC/packaging/sysconfig/sball-addition"     "$STAGE/etc/sysconfig/sball-addition"
install -m 0644 "$SRC/packaging/logrotate.d/sball-addition"   "$STAGE/etc/logrotate.d/sball-addition"
install -m 0644 "$SRC/packaging/systemd/sballd.service.d/sball-addition.conf" \
    "$STAGE/etc/systemd/system/sballd.service.d/sball-addition.conf"
install -m 0644 "$SRC/packaging/systemd/sball-addition-watcher.service" \
    "$STAGE/usr/lib/systemd/system/sball-addition-watcher.service"
install -m 0644 "$SRC/packaging/systemd/sball-addition-metrics.service" \
    "$STAGE/usr/lib/systemd/system/sball-addition-metrics.service"
install -m 0644 "$SRC/packaging/systemd/sball-addition-metrics.timer" \
    "$STAGE/usr/lib/systemd/system/sball-addition-metrics.timer"
install -d "$STAGE/usr/share/man/man8"
for page in sball-addition sballd-wrapper sball-node-verify sball-rollout; do
    install -m 0644 "$SRC/docs/$page.8" "$STAGE/usr/share/doc/sball-addition/$page.8"
    # -n keeps the gzip header free of a timestamp (reproducible packages)
    gzip -9n -c "$SRC/docs/$page.8" >"$STAGE/usr/share/man/man8/$page.8.gz"
done
gzip -9n -c "$SRC/packaging/debian/changelog" >"$STAGE/usr/share/doc/sball-addition/changelog.Debian.gz"
install -d "$STAGE/usr/share/lintian/overrides"
install -m 0644 "$HERE/lintian-overrides" "$STAGE/usr/share/lintian/overrides/sball-addition"
install -m 0644 "$SRC/packaging/debian/copyright" "$STAGE/usr/share/doc/sball-addition/copyright"
install -m 0644 "$SRC/docs/hook-template.sh"        "$STAGE/usr/share/doc/sball-addition/hook-template.sh"
[[ -f "$SRC/PRODUCTION.md" ]] && install -m 0644 "$SRC/PRODUCTION.md" "$STAGE/usr/share/doc/sball-addition/PRODUCTION.md"
[[ -f "$SRC/README.md" ]] && install -m 0644 "$SRC/README.md" "$STAGE/usr/share/doc/sball-addition/README.md"

# ----------------------------------------------------------- control data ---
INSTALLED_KB=$(du -sk "$STAGE" | awk '{print $1}')
cat >"$STAGE/DEBIAN/control" <<EOF
Package: sball-addition
Version: $VERSION-$RELEASE
Section: admin
Priority: optional
Architecture: $ARCH
Maintainer: $MAINTAINER
Depends: bash (>= 4.2), systemd
Installed-Size: $INSTALLED_KB
Homepage: https://www.cloudavid.com/
Description: managed hook system for the sballd (PVM) service
 Runs administrator supplied scripts at two points of the sballd lifecycle:
 pre-quorate (before the service starts) and post-quorate (after the node
 joined the cluster). Ships a CLI, a service wrapper, a quorate watcher, JSON
 logging, timeouts, retries, RUN_ONCE markers, cluster aware targets, a node
 acceptance checker, a cluster rollout tool and a Prometheus exporter.
 .
 The package does not modify sballd.service: the ExecStart override is a
 conffile you can delete to get back to a plain daemon start.
EOF

# conffiles: everything dpkg must not silently overwrite on upgrades
{
    echo /etc/sball-addition/sball-addition.conf
    echo /etc/sball-addition/lib/common.sh
    for hook in "$SRC"/hooks/pre-quorate.d/*.sh; do
        echo "/etc/sball-addition/hooks.d/pre-quorate.d/$(basename "$hook")"
    done
    for hook in "$SRC"/hooks/post-quorate.d/*.sh; do
        echo "/etc/sball-addition/hooks.d/post-quorate.d/$(basename "$hook")"
    done
    echo /etc/sysconfig/sball-addition
    echo /etc/logrotate.d/sball-addition
    echo /etc/systemd/system/sballd.service.d/sball-addition.conf
} >"$STAGE/DEBIAN/conffiles"

# md5sums for every file (dpkg verifies them; lintian complains otherwise)
( cd "$STAGE" && find . -path ./DEBIAN -prune -o -type f -print |
    sed 's|^\./||' | sort | xargs md5sum >"$STAGE/DEBIAN/md5sums" ) 2>/dev/null

install -m 0755 "$HERE/postinst" "$STAGE/DEBIAN/postinst"
install -m 0755 "$HERE/prerm" "$STAGE/DEBIAN/prerm"
install -m 0755 "$HERE/postrm" "$STAGE/DEBIAN/postrm"

# ---------------------------------------------------------------- build -----
mkdir -p "$DIST"
DEB="$DIST/sball-addition_${VERSION}-${RELEASE}_${ARCH}.deb"
dpkg-deb --root-owner-group --build "$STAGE" "$DEB" >/dev/null
echo "wrote $DEB"
dpkg-deb --info "$DEB" | sed -n '1,14p'
