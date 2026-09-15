#!/usr/bin/env bash
# Build ONE protocol's client image (buildkit -> skopeo -> forgejo).
#
#   ./build-one.sh base            # the shared debian base
#   ./build-one.sh npm [tag]       # a protocol image (defaults to base:latest)
#
# The Dockerfile is Dockerfile.<proto> (base uses base/Dockerfile). Every
# toolchain tarball is fetched from easylab's generic store, so the build needs
# no egress proxy; only the base layer's apt step touches the public internet.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PROTO="${1:?usage: build-one.sh <proto|base> [tag]}"
TAG="${2:-latest}"

REGISTRY="${REGISTRY:-forgejo.develop.10.199.64.20.nip.io}"
NAMESPACE="${NAMESPACE:-easylab}"
BUILDKIT="${BUILDKIT_ADDR:-tcp://buildkitd.temp.svc.cluster.local:1234}"
FORGEJO_USER="${FORGEJO_USER:-root}"
FORGEJO_PASS="${FORGEJO_PASS:-devpassword}"
EASYLAB="${EASYLAB:-http://easylab.temp.svc.cluster.local}"
APT_PROXY="${APT_PROXY:-}"
BASE_IMAGE="${BASE_IMAGE:-${REGISTRY}/${NAMESPACE}/toolchain-base:latest}"

if [ "$PROTO" = "base" ]; then
  NAME="toolchain-base"; DOCKERFILE="${HERE}/base/Dockerfile"
else
  NAME="tool-${PROTO}"; DOCKERFILE="${HERE}/Dockerfile.${PROTO}"
fi
[ -f "$DOCKERFILE" ] || { echo "no Dockerfile for ${PROTO}: ${DOCKERFILE}"; exit 1; }
DEST="${REGISTRY}/${NAMESPACE}/${NAME}:${TAG}"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
CTX="${WORK}/ctx"; mkdir -p "${CTX}"
cp "${DOCKERFILE}" "${CTX}/"
[ -d "${HERE}/base" ] && cp -r "${HERE}/base/." "${CTX}/" 2>/dev/null || true

echo "== build ${NAME}:${TAG} on buildkit ${BUILDKIT} =="
buildctl --addr "${BUILDKIT}" build \
  --frontend dockerfile.v0 \
  --local "context=${CTX}" \
  --local "dockerfile=${CTX}" \
  --opt "filename=$(basename "${DOCKERFILE}")" \
  --opt "build-arg:EASYLAB=${EASYLAB}" \
  --opt "build-arg:BASE_IMAGE=${BASE_IMAGE}" \
  --opt "build-arg:APT_PROXY=${APT_PROXY}" \
  --output "type=docker,name=${NAMESPACE}/${NAME}:${TAG},dest=${WORK}/image.tar" \
  --progress plain

echo "== push ${DEST} =="
skopeo copy --dest-creds "${FORGEJO_USER}:${FORGEJO_PASS}" --dest-tls-verify=false \
  "docker-archive:${WORK}/image.tar:${NAMESPACE}/${NAME}:${TAG}" "docker://${DEST}"
skopeo inspect --creds "${FORGEJO_USER}:${FORGEJO_PASS}" --tls-verify=false "docker://${DEST}" >/dev/null \
  && echo "OK ${DEST}"
