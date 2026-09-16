#!/bin/sh
# conan lifecycle: conan create+upload / download public+private / upgrade / remove
set -e
export CONAN_HOME="$HOME/conan"
mkdir -p "$CONAN_HOME"
conan profile detect --force >/dev/null 2>&1
conan remote add easylab https://center.conan.io --force >/dev/null 2>&1 || true

REF="lc-probe-${SUFFIX}"

make_recipe() { # $1 = version
  mkdir -p "$WORK/recipe"
  cat > "$WORK/recipe/conanfile.py" <<EOF
import os
from conan import ConanFile

class LcProbeConan(ConanFile):
    name = "$REF"
    version = "$1"
    no_copy_source = True

    def package(self):
        res = os.path.join(self.package_folder, "res")
        os.makedirs(res)
        with open(os.path.join(res, "version.txt"), "w") as f:
            f.write(self.version)
EOF
}

create_and_upload() { # $1 = version
  make_recipe "$1"
  conan create "$WORK/recipe" --build=missing >/dev/null
  conan upload "$REF/$1" -r easylab -c >/dev/null
}

case "$STAGE" in
publish)
  create_and_upload "$V1"
  echo "conan: uploaded $REF@$V1"
  ;;
public)
  conan download zlib/1.3.1 -r easylab --only-recipe >/dev/null
  conan list "*zlib*" -r easylab 2>/dev/null | grep -q "1.3.1"
  echo "conan: public zlib/1.3.1 ok"
  ;;
private)
  conan download "$REF/$V1" -r easylab >/dev/null
  out="$(conan list "*lc-probe*" -r easylab 2>/dev/null)"
  echo "$out" | grep -q "$V1" || { echo "conan: $V1 not listed: $out"; exit 1; }
  echo "conan: private $REF@$V1 ok"
  ;;
upgrade)
  create_and_upload "$V2"
  conan download "$REF/$V2" -r easylab >/dev/null
  out="$(conan list "*lc-probe*" -r easylab 2>/dev/null)"
  echo "$out" | grep -q "$V1" || { echo "conan: $V1 dropped: $out"; exit 1; }
  echo "$out" | grep -q "$V2" || { echo "conan: $V2 missing: $out"; exit 1; }
  echo "conan: upgraded to $V2, both listed"
  ;;
delete)
  conan remove "$REF/*" -r easylab -c >/dev/null
  out="$(conan list "*lc-probe*" -r easylab 2>/dev/null)"
  echo "$out" | grep -q "$V1" && { echo "conan: still listed after remove: $out"; exit 1; }
  if conan download "$REF/$V1" -r easylab >/dev/null 2>&1; then
    echo "conan: still downloadable after remove"; exit 1
  fi
  echo "conan: removed $REF"
  ;;
esac
