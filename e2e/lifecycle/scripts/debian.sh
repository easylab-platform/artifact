#!/bin/sh
# debian lifecycle: build+PUT a .deb into a hosted flat repo /artifacts/debian/
# <repo>, then apt-get install it from that repo (public+private+upgrade), and
# delete via DELETE.
set -e
export HOME="/tmp/lc-home-${STAGE}"
apt-get -o Acquire::http::Proxy=http://127.0.0.1:80 update -o Acquire::Retries=0 >/dev/null 2>&1 || true

REPO=easylab
PKG="lc-probe-${SUFFIX}"
PKGDIR="lc-probe-${SUFFIX}"
ARCH=amd64
KEYRING=/etc/apt/keyrings/lc.gpg
SRC=/etc/apt/sources.list.d/lc.list

pick_arch() {
  # APT_ARCH keeps the toolchain-native arch (debian12/debian13 images are
  # amd64, but keep this honest if the matrix ever moves).
  ARCH="$(dpkg --print-architecture)"
}

build_deb() { # $1 = version
  rm -rf "$WORK/pkg"
  mkdir -p "$WORK/pkg/DEBIAN" "$WORK/pkg/usr/bin"
  cd "$WORK/pkg"
  cat > "DEBIAN/control" <<X
Package: ${PKGDIR}
Version: $1
Architecture: ${ARCH}
Maintainer: lc <lc@easylab.invalid>
Description: lc probe package ${SUFFIX}
Section: misc
Priority: optional
X
  printf '#!/bin/sh\necho "lc-probe %s"\n' "$1" > "$PKGDIR"
  mv "$PKGDIR" usr/bin/
  chmod 0755 "usr/bin/${PKGDIR}"
  dpkg-deb --build --root-owner-group . "$WORK/${PKGDIR}_$1_${ARCH}.deb" >/dev/null
}

publish_deb() { # $1 = version
  build_deb "$1"
  # Upload through the SAME spoofed origin the client uses, so the sidecar
  # maps deb.debian.org/<repo>/... onto /artifacts/debian/<repo>/... (hosted repo
  # "easylab"). This keeps the URL shape identical to the pull-through path.
  code="$(curl -s -o /dev/null -w '%{http_code}' -X PUT \
    --data-binary "@$WORK/${PKGDIR}_$1_${ARCH}.deb" \
    "http://deb.debian.org/${REPO}/${PKGDIR}_$1_${ARCH}.deb")"
  [ "$code" = "201" ] || [ "$code" = "200" ] || { echo "debian: upload rc=$code"; exit 1; }
}

configure_repo() {
  printf 'deb [trusted=yes] http://deb.debian.org/%s ./\n' "$REPO" > "$SRC"
}

install_pkg() { # $1 = expected version
  export PATH="/usr/local/bin:/usr/bin:/bin:$PATH"
  apt-get -o Acquire::http::Proxy=http://127.0.0.1:80 -o Acquire::Retries=0 \
    -o Dir::Etc::sourcelist="$SRC" -o Dir::Etc::sourceparts="-" -o APT::Get::List-Cleanup="0" \
    update >/dev/null 2>&1
  apt-get -o Acquire::http::Proxy=http://127.0.0.1:80 -o Acquire::Retries=0 \
    -o Dir::Etc::sourcelist="$SRC" -o Dir::Etc::sourceparts="-" \
    install -y --allow-unauthenticated --reinstall "${PKGDIR}" >/dev/null 2>&1
  out="$("$PKGDIR" 2>&1 | tail -1)"
  echo "$out" | grep -q "$1" || { echo "debian: got '$out' want $1"; return 1; }
  echo "debian: ${PKGDIR} $1 ok"
}

APT_PROXY="http://127.0.0.1:80"
APT_OPTS="-o Acquire::http::Proxy=$APT_PROXY -o Acquire::Retries=0"

case "$STAGE" in
publish)
  pick_arch
  publish_deb "$V1"
  echo "debian: published ${PKGDIR}@$V1"
  ;;
public)
  # Pull-through: install a stock package from deb.debian.org (spoofed).
  apt-get $APT_OPTS update >/dev/null 2>&1
  apt-get $APT_OPTS install -y --no-install-recommends jq >/dev/null 2>&1
  echo '{"ok":true}' | jq -e '.ok' >/dev/null
  echo "debian: public jq $(jq --version) installed"
  ;;
private)
  configure_repo
  install_pkg "$V1"
  ;;
upgrade)
  pick_arch
  publish_deb "$V2"
  configure_repo
  install_pkg "$V2"
  ;;
delete)
  pick_arch
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
    "http://deb.debian.org/${REPO}/${PKGDIR}_${V2}_${ARCH}.deb")"
  [ "$code" = "204" ] || [ "$code" = "200" ] || { echo "debian: delete rc=$code"; exit 1; }
  idx="$(curl -s "http://deb.debian.org/${REPO}/Packages")"
  echo "$idx" | grep -q "$V2" && { echo "debian: $V2 still indexed"; exit 1; }
  echo "debian: deleted ${PKGDIR}@$V2"
  ;;
esac
