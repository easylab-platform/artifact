#!/bin/sh
# Exercise the blob redirect path: resolve an image manifest list, then GET a
# layer blob (the registry 307-redirects to a CDN; artifact follows, verifies,
# and caches it). The digest is then verified locally.
set -e
cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt
REG=https://registry-1.docker.io
REPO=library/busybox
IDX=$(wget -qO- --header='Accept: application/vnd.docker.distribution.manifest.list.v2+json' "$REG/v2/$REPO/manifests/latest")
AMD=$(printf '%s' "$IDX" | tr '}' '\n' | grep -o 'sha256:[a-f0-9]\{64\}' | head -1)
M=$(wget -qO- --header='Accept: application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.v2+json' "$REG/v2/$REPO/manifests/$AMD")
L=$(printf '%s' "$M" | tr ',' '\n' | grep -o 'sha256:[a-f0-9]\{64\}' | tail -1)
echo "layer=$L"
wget -qO /tmp/layer "$REG/v2/$REPO/blobs/$L"
ls -l /tmp/layer
# Assertion: the blob's content hash matches the digest it was fetched under,
# and it is a valid gzip stream.
GOT="sha256:$(sha256sum /tmp/layer | cut -d' ' -f1)"
[ "$GOT" = "$L" ] || { echo "digest mismatch: got $GOT want $L"; exit 1; }
gzip -t /tmp/layer
echo "oci: verified $L"
