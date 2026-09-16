#!/bin/sh
# rpm lifecycle: rpmbuild a real .rpm + PUT it into a hosted repo
# /pkgs/rpm/<repo>/<arch>/, then `dnf install` it (public+private+upgrade) and
# delete via DELETE.
set -e
export HOME="/tmp/lc-home-${STAGE}"
cp /etc/easyproxy/ca.crt /etc/pki/ca-trust/source/anchors/easylab.crt 2>/dev/null || true
update-ca-trust 2>/dev/null || true

REPO=easylab
ARCH=noarch
PKG="lc-probe-${SUFFIX}"
ORIGIN=https://dl.fedoraproject.org

repo_url() { echo "${ORIGIN}/${REPO}"; }

ensure_tools() {
  rm -f /etc/yum.repos.d/*.repo
  printf '[main]\ninstall_weak_deps=False\nreleasever=44\n' > /etc/dnf/dnf.conf
  printf '[fedora]\nname=fedora\nbaseurl=%s/pub/fedora/linux/releases/43/Everything/x86_64/os/\nenabled=1\ngpgcheck=0\noptional_metadata_types=primary\n' "$ORIGIN" > /etc/yum.repos.d/fedora.repo
  rpm -q rpm-build >/dev/null 2>&1 || dnf -y --refresh install rpm-build >/dev/null 2>&1
}

build_rpm() { # $1 = version
  rm -rf "$WORK/rpmbuild"
  mkdir -p "$WORK/rpmbuild/BUILD" "$WORK/rpmbuild/RPMS" \
           "$WORK/rpmbuild/SOURCES" "$WORK/rpmbuild/SPECS" "$WORK/rpmbuild/SRPMS"
  cd "$WORK/rpmbuild/SPECS"
  cat > "${PKG}.spec" <<X
Name: ${PKG}
Version: $1
Release: 1
Summary: lc probe package ${SUFFIX}
License: MIT
BuildArch: ${ARCH}
%description
lc probe package ${SUFFIX}
%install
mkdir -p %{buildroot}/usr/bin
cat > %{buildroot}/usr/bin/${PKG} <<SCRIPT
#!/bin/sh
echo "lc-probe"$1
SCRIPT
chmod 0755 %{buildroot}/usr/bin/${PKG}
%files
/usr/bin/${PKG}
X
  rpmbuild -bb --define "_topdir $WORK/rpmbuild" "${PKG}.spec" >/dev/null 2>&1
}

publish_rpm() { # $1 = version
  build_rpm "$1"
  rpm="$WORK/rpmbuild/RPMS/${ARCH}/${PKG}-$1-1.${ARCH}.rpm"
  [ -f "$rpm" ] || { echo "rpm: built rpm not found"; exit 1; }
  code="$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary "@$rpm" \
    "${ORIGIN}/${REPO}/${ARCH}/${PKG}-$1-1.${ARCH}.rpm")"
  [ "$code" = "201" ] || [ "$code" = "200" ] || { echo "rpm: upload rc=$code"; exit 1; }
}

configure_repo() {
  printf '[easylab]\nname=easylab\nbaseurl=%s\nenabled=1\ngpgcheck=0\noptional_metadata_types=primary\n' "$(repo_url)" > /etc/yum.repos.d/easylab.repo
}

install_pkg() { # $1 = expected version
  configure_repo
  dnf -y --refresh --disablerepo=fedora install "${PKG}" >/dev/null 2>&1 || \
    dnf -y --refresh install "${PKG}" >/dev/null 2>&1
  out="$("${PKG}" 2>&1 | tail -1)"
  echo "$out" | grep -q "$1" || { echo "rpm: got '$out' want $1"; return 1; }
  echo "rpm: ${PKG} $1 ok"
}

ensure_tools

case "$STAGE" in
publish)
  publish_rpm "$V1"
  echo "rpm: published ${PKG}@$V1"
  ;;
public)
  dnf -y --refresh install jq >/dev/null 2>&1
  echo '{"ok":true}' | jq -e '.ok' >/dev/null
  echo "rpm: public jq $(jq --version) installed"
  ;;
private)
  install_pkg "$V1"
  ;;
upgrade)
  publish_rpm "$V2"
  install_pkg "$V2"
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
    "${ORIGIN}/${REPO}/${ARCH}/${PKG}-${V2}-1.${ARCH}.rpm")"
  [ "$code" = "204" ] || [ "$code" = "200" ] || { echo "rpm: delete rc=$code"; exit 1; }
  prim="$(curl -s "$(repo_url)/repodata/repomd.xml" | grep -oE 'repodata/[^"]+' | head -1)"
  if curl -s "$(repo_url)/$prim" | gunzip -c | grep -q "${PKG}-${V2}"; then
    echo "rpm: $V2 still in primary metadata"; exit 1
  fi
  echo "rpm: deleted ${PKG}@$V2"
  ;;
esac
