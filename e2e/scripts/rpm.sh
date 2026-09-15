#!/bin/sh
set -e
# rpm/dnf come from Debian apt; the client points at the real Fedora mirror
# host, which the spoofed proxy intercepts (MITM -> artifact-rpm). The full
# filelists metadata is ~16MB, so the repo disables it and requests only
# primary metadata.
cp /etc/easyproxy/ca.crt /usr/local/share/ca-certificates/easylab.crt
update-ca-certificates >/dev/null 2>&1 || cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt
mkdir -p /etc/yum.repos.d
rm -f /etc/yum.repos.d/*.repo /etc/dnf/dnf.conf
printf '[main]\nsslverify=1\ninstall_weak_deps=False\nreleasever=43\n' > /etc/dnf/dnf.conf
printf '[fedora]\nname=fedora\nbaseurl=https://dl.fedoraproject.org/pub/fedora/linux/releases/43/Everything/x86_64/os/\nenabled=1\ngpgcheck=0\noptional_metadata_types=primary\n' > /etc/yum.repos.d/fedora.repo
# Download (do not install) the RPM: installing Fedora packages onto the
# Debian root would clobber glibc. This exercises metadata resolution and the
# package download through the proxy.
rm -rf /tmp/rpms && mkdir -p /tmp/rpms
dnf -y --refresh install --downloadonly --destdir=/tmp/rpms jq
ls -l /tmp/rpms
