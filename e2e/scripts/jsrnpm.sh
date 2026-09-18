#!/bin/sh
# JSR npm-compatibility registry (npm.jsr.io): deno/bun/npm resolve @jsr/* here
# as ordinary npm packuments. Fetch the packument, then follow the dist.tarball
# URL it advertises (the adapter rewrites it to itself, so this exercises the
# packument-tarball fallback: JSR serves tarballs under /~/<id>/..., not the
# conventional /<name>/-/<file>).
set -e
CA=/etc/easysidecar/ca.crt
wget --ca-certificate=$CA -qO /tmp/packument.json "https://npm.jsr.io/@jsr%2Fluca__flag"
grep -q '"name":"@jsr/luca__flag"' /tmp/packument.json || { echo "jsrnpm: bad packument"; exit 1; }
tarball=$(sed -n 's/.*"tarball":"\([^"]*\)".*/\1/p' /tmp/packument.json | head -1)
[ -n "$tarball" ] || { echo "jsrnpm: no tarball in packument"; exit 1; }
wget --ca-certificate=$CA -qO /tmp/pkg.tgz "$tarball"
tar -tzf /tmp/pkg.tgz >/dev/null || { echo "jsrnpm: bad tarball"; exit 1; }
echo "jsrnpm: packument + tarball ok ($tarball)"
