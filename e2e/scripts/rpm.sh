#!/bin/sh
set -e
cp /etc/easyproxy/ca.crt /etc/pki/ca-trust/source/anchors/easylab.crt
update-ca-trust
rm -f /etc/yum.repos.d/*.repo
printf '[fedora]\nname=fedora\nbaseurl=https://dl.fedoraproject.org/pub/fedora/linux/releases/43/Everything/x86_64/os/\nenabled=1\ngpgcheck=0\n' > /etc/yum.repos.d/f.repo
dnf -y --refresh install jq
