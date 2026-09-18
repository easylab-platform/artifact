#!/bin/sh
# OCI multi-registry: the registry is carried in the request Host (a docker
# client never puts it in the path), so one gateway mirrors any registry a
# rule steers to it. This resolves a manifest from each configured registry
# and verifies a layer blob from Docker Hub (whose blob endpoint 307-redirects
# to a CDN that artifact follows, hashes and caches).
set -e
cat /etc/easysidecar/ca.crt >> /etc/ssl/certs/ca-certificates.crt

# fetch_manifest <registry> <repo> <tag>: pull the manifest list and print the
# first platform digest, proving the registry resolved through the gateway.
fetch_manifest() {
  wget -qO- --header='Accept: application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.index.v1+json' \
    "https://$1/v2/$2/manifests/$3" | tr '}' '\n' | grep -o 'sha256:[a-f0-9]\{64\}' | head -1
}

# Docker Hub: full path (manifest list -> amd64 manifest -> layer blob).
D=$(fetch_manifest registry-1.docker.io library/busybox latest)
[ -n "$D" ] || { echo "oci: no docker.io manifest digest"; exit 1; }
M=$(wget -qO- --header='Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
  "https://registry-1.docker.io/v2/library/busybox/manifests/$D")
L=$(printf '%s' "$M" | tr ',' '\n' | grep -o 'sha256:[a-f0-9]\{64\}' | tail -1)
wget -qO /tmp/layer "https://registry-1.docker.io/v2/library/busybox/blobs/$L"
GOT="sha256:$(sha256sum /tmp/layer | cut -d' ' -f1)"
[ "$GOT" = "$L" ] || { echo "oci: digest mismatch $GOT != $L"; exit 1; }
gzip -t /tmp/layer

# More registries through the same gateway prove the request Host selects the
# upstream. (registry.k8s.io 307-redirects its manifest endpoint, so it is not
# included until manifest redirects are followed.)
for pair in "ghcr.io/astral-sh/uv:latest" "quay.io/prometheus/prometheus:latest" \
            "mcr.microsoft.com/hello-world:latest"; do
  reg=${pair%%/*}
  rest=${pair#*/}
  repo=${rest%%:*}
  tag=${rest##*:}
  if d=$(fetch_manifest "$reg" "$repo" "$tag") && [ -n "$d" ]; then
    echo "oci: $reg/$repo:$tag -> ${d%:*}..."
  else
    echo "oci: $reg/$repo:$tag not resolvable"; exit 1
  fi
done

echo "oci: verified $L from docker.io + 3 more registries"
