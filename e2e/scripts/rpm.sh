#!/bin/sh
set -e
# Native Fedora base: dnf/rpm work as-is. Trust the egress CA, point the repo
# at the real mirror (intercepted by the spoofed proxy -> artifact-rpm), and
# install a package. filelists metadata is skipped (only primary is needed).
cp /etc/easysidecar/ca.crt /etc/pki/ca-trust/source/anchors/easylab.crt
update-ca-trust
rm -f /etc/yum.repos.d/*.repo
printf '[main]\ninstall_weak_deps=False\nreleasever=44\n' > /etc/dnf/dnf.conf
printf '[fedora]\nname=fedora\nbaseurl=https://dl.fedoraproject.org/pub/fedora/linux/releases/43/Everything/x86_64/os/\nenabled=1\ngpgcheck=0\noptional_metadata_types=primary\n' > /etc/yum.repos.d/fedora.repo
dnf -y --refresh install jq
rpm -q jq
# Run assertion: execute the installed binary.
echo '{"ok":true}' | jq -e '.ok' >/dev/null
echo "dnf installed: $(jq --version)"
