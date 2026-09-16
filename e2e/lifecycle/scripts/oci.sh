#!/bin/sh
# oci lifecycle: skopeo copy (push) / inspect public+private / upgrade / delete
# Pushing re-tags cached public images — a genuine blob+manifest upload through
# the spoofed registry-1.docker.io.
set -e
REG=registry-1.docker.io
REPO="$REG/lc-probe-${SUFFIX}"
# Two distinct public images so v1/v2 have different manifests.
SRC_V1=docker://busybox:1.36
SRC_V2=docker://busybox:musl

case "$STAGE" in
publish)
  skopeo copy --retry-times 5 "$SRC_V1" "docker://$REPO:$V1" >/dev/null
  d1="$(skopeo inspect --format '{{.Digest}}' "docker://$REPO:$V1")"
  echo "oci: pushed $REPO:$V1 ($d1)"
  ;;
public)
  d="$(skopeo inspect --format '{{.Digest}}' docker://busybox:latest)"
  echo "oci: public busybox ($d)"
  ;;
private)
  d1="$(skopeo inspect --format '{{.Digest}}' "docker://$REPO:$V1")"
  [ -n "$d1" ] || { echo "oci: no digest for $V1"; exit 1; }
  # The manifest really round-trips: pull a config blob.
  skopeo inspect --config "docker://$REPO:$V1" | grep -q architecture
  echo "oci: private $REPO:$V1 ($d1) ok"
  ;;
upgrade)
  skopeo copy --retry-times 5 "$SRC_V2" "docker://$REPO:$V2" >/dev/null
  d1="$(skopeo inspect --format '{{.Digest}}' "docker://$REPO:$V1")"
  d2="$(skopeo inspect --format '{{.Digest}}' "docker://$REPO:$V2")"
  [ "$d1" != "$d2" ] || { echo "oci: tags collapsed to one digest"; exit 1; }
  echo "oci: $V1=$d1 $V2=$d2"
  ;;
delete)
  d1="$(skopeo inspect --format '{{.Digest}}' "docker://$REPO:$V1")"
  skopeo delete "docker://$REPO:$V1"
  if skopeo inspect "docker://$REPO:$V1" >/dev/null 2>&1; then
    echo "oci: $V1 still present after delete"; exit 1
  fi
  # Deleting v1 (by digest) must not take v2 with it.
  d2="$(skopeo inspect --format '{{.Digest}}' "docker://$REPO:$V2")"
  [ -n "$d2" ] || { echo "oci: $V2 lost after deleting $V1"; exit 1; }
  echo "oci: deleted $V1 ($d1), $V2 survives"
  ;;
esac
