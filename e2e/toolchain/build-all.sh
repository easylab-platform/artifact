#!/usr/bin/env bash
# Build every protocol image one at a time (base must already be built).
# Logs each to /tmp/tc-<proto>.log and stops on the first failure.
#
# VARIANT selects the debian base (bookworm|trixie); rpm and apk are always
# built from their native distro images and are skipped by the trixie variant.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VARIANT="${VARIANT:-bookworm}"

if [ "$VARIANT" = "trixie" ]; then
  PROTOS="${*:-npm pypi huggingface conan go cargo maven nuget rubygems composer hex pub helm protobuf conda oci swift debian}"
else
  PROTOS="${*:-npm pypi huggingface conan go cargo maven nuget rubygems composer hex pub helm protobuf conda oci swift debian rpm apk nix}"
fi

fail=0
for p in $PROTOS; do
  echo "===== building tool-${p} (${VARIANT}) ====="
  if ! VARIANT="$VARIANT" "${HERE}/build-one.sh" "$p" > "/tmp/tc-${VARIANT}-${p}.log" 2>&1; then
    echo "FAIL ${p} (see /tmp/tc-${VARIANT}-${p}.log)"; tail -5 "/tmp/tc-${VARIANT}-${p}.log"; fail=1
    break
  fi
  echo "OK   ${p}"
done
[ "$fail" -eq 0 ] && echo "ALL BUILT (${VARIANT})" || echo "STOPPED ON FAILURE"
