#!/usr/bin/env bash
# Build every protocol image one at a time (base must already be built).
# Logs each to /tmp/tc-<proto>.log and stops on the first failure.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROTOS="${*:-npm pypi huggingface conan go cargo maven nuget rubygems composer hex pub helm protobuf conda oci swift debian rpm apk nix}"
fail=0
for p in $PROTOS; do
  echo "===== building tool-${p} ====="
  if ! "${HERE}/build-one.sh" "$p" latest > "/tmp/tc-${p}.log" 2>&1; then
    echo "FAIL ${p} (see /tmp/tc-${p}.log)"; tail -5 "/tmp/tc-${p}.log"; fail=1
    break
  fi
  echo "OK   ${p}"
done
[ "$fail" -eq 0 ] && echo "ALL BUILT" || echo "STOPPED ON FAILURE"
