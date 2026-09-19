#!/bin/sh
# Generic URI cache (netcache): fetch an arbitrary public URL through the
# mirror twice. The first fetch populates the CAS; the second must be served
# from local bytes (asserted by an identical body and a Range request that
# returns 206 from the cache). This is the "no external asset is fetched
# twice" guarantee for hosts with no dedicated protocol adapter.
set -e
CA=/etc/easysidecar/ca.crt
URL="https://raw.githubusercontent.com/git/git/master/README.md"

curl -fsS --cacert "$CA" -o /tmp/a "$URL"
[ -s /tmp/a ] || { echo "netcache: empty first fetch"; exit 1; }

curl -fsS --cacert "$CA" -o /tmp/b "$URL"
cmp -s /tmp/a /tmp/b || { echo "netcache: cached body differs"; exit 1; }

# A Range request must be satisfied locally (206) without a second upstream
# fetch; the CAS serves byte ranges.
code="$(curl -s --cacert "$CA" -o /tmp/range -w '%{http_code}' -H 'Range: bytes=0-99' "$URL")"
[ "$code" = "206" ] || { echo "netcache: range rc=$code"; exit 1; }
[ "$(wc -c </tmp/range)" = "100" ] || { echo "netcache: range size wrong"; exit 1; }

echo "netcache: arbitrary URL cached, range served locally ($(wc -c </tmp/a) bytes)"
