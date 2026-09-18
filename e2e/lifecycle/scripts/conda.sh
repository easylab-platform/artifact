#!/bin/sh
# conda lifecycle: build a real .tar.bz2 package + PUT it into a hosted
# channel /artifacts/conda/<channel>/<subdir>/, then `conda create` an env with it
# (public+private+upgrade) and delete via DELETE.
set -e
export HOME="/tmp/lc-home-${STAGE}"
export CONDARC="$HOME/.condarc"
mkdir -p "$HOME"

REPO=easylab
SUBDIR=linux-64
PKG="lc-probe-${SUFFIX}"
ORIGIN=https://conda.anaconda.org

channel_url() { echo "${ORIGIN}/${REPO}"; }

build_pkg() { # $1 = version
  rm -rf "$WORK/pkg"
  mkdir -p "$WORK/pkg/info" "$WORK/pkg/bin"
  python - "$WORK/pkg" "$PKG" "$1" "$SUBDIR" <<'PY'
import json, os, sys
pkgdir, name, ver, subdir = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
json.dump({"name": name, "version": ver, "build": "0", "build_number": 0,
           "depends": [], "subdir": subdir, "license": "MIT"},
          open(os.path.join(pkgdir, "info", "index.json"), "w"))
# No paths.json: this conda only supports the version-1 schema, and a
# package without it installs fine (only --use-only-tar-bz2 style checks care).
open(os.path.join(pkgdir, "info", "files"), "w").write("bin/%s\n" % name)
with open(os.path.join(pkgdir, "bin", name), "w") as f:
    f.write("#!/bin/sh\necho lc-probe %s\n" % ver)
os.chmod(os.path.join(pkgdir, "bin", name), 0o755)
PY
  ( cd "$WORK/pkg" && tar -cjf "$WORK/${PKG}-$1-0.tar.bz2" . )
}

publish_pkg() { # $1 = version
  build_pkg "$1"
  code="$(curl -s -o /dev/null -w '%{http_code}' -X PUT \
    --data-binary "@$WORK/${PKG}-$1-0.tar.bz2" \
    "$(channel_url)/${SUBDIR}/${PKG}-$1-0.tar.bz2")"
  [ "$code" = "201" ] || [ "$code" = "200" ] || { echo "conda: upload rc=$code"; exit 1; }
}

use_env() { # $1 = expected version
  rm -rf "$HOME/envs/lc" "$WORK/use"
  conda create -y -q -p "$WORK/use" --override-channels -c "$(channel_url)" "$PKG" >/dev/null 2>&1
  out="$("$WORK/use/bin/${PKG}" 2>&1 | tail -1)"
  echo "$out" | grep -q "$1" || { echo "conda: got '$out' want $1"; return 1; }
  echo "conda: ${PKG} $1 ok"
}

case "$STAGE" in
publish)
  publish_pkg "$V1"
  echo "conda: published ${PKG}@$V1"
  ;;
public)
  conda tos accept --override-channels --channel https://repo.anaconda.com/pkgs/main >/dev/null 2>&1 || true
  conda tos accept --override-channels --channel https://repo.anaconda.com/pkgs/r >/dev/null 2>&1 || true
  rm -rf "$WORK/pub"
  conda create -y -q -p "$WORK/pub" --override-channels -c defaults python zlib >/dev/null 2>&1
  "$WORK/pub/bin/python" -c 'import zlib; print("conda public zlib:", zlib.ZLIB_VERSION)'
  ;;
private)
  use_env "$V1"
  ;;
upgrade)
  publish_pkg "$V2"
  use_env "$V2"
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
    "$(channel_url)/${SUBDIR}/${PKG}-${V2}-0.tar.bz2")"
  [ "$code" = "204" ] || [ "$code" = "200" ] || { echo "conda: delete rc=$code"; exit 1; }
  repodata="$(curl -s "$(channel_url)/${SUBDIR}/repodata.json")"
  echo "$repodata" | grep -q "${PKG}-${V2}-0" && { echo "conda: $V2 still in repodata"; exit 1; }
  echo "conda: deleted ${PKG}@$V2"
  ;;
esac
