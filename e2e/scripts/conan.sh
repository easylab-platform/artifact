#!/bin/sh
set -e
# Point conan at the REAL ConanCenter host; the spoofed proxy intercepts it
# (MITM + add_prefix /artifacts/conan) and steers it to artifact-conan.
export CONAN_HOME=/tmp/conanhome
mkdir -p "$CONAN_HOME"
conan profile detect --force >/dev/null 2>&1
conan remote add easylab https://center.conan.io --force >/dev/null 2>&1 || true
conan download zlib/1.3.1 -r easylab --only-recipe
# Assertion: the pulled recipe is valid and inspectable (conan's own parser).
conan inspect zlib/1.3.1 | head -5
