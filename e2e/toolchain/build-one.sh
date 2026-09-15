#!/usr/bin/env bash
# Build ONE protocol's client image (buildkit -> skopeo -> forgejo).
#
#   ./build-one.sh base [tag]         # the shared debian base
#   ./build-one.sh npm  [tag]         # a protocol image
#
# Variants (so bookworm and trixie coexist):
#   VARIANT=bookworm (default)  BASE_SLIM=root/debian:bookworm-slim  tags :latest
#   VARIANT=trixie              BASE_SLIM=root/debian:trixie-slim    tags :trixie
# For a protocol build, BASE_TAG picks which base to sit on (defaults to the
# variant's tag). The Dockerfile is Dockerfile.<proto> (base uses base/Dockerfile).
# Every toolchain tarball is fetched from easylab's generic store, so the build
# needs no egress proxy; only the base layer's apt step touches the internet.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

PROTO="${1:?usage: build-one.sh <proto|base> [tag]}"

REGISTRY="${REGISTRY:-forgejo.develop.10.199.64.20.nip.io}"
NAMESPACE="${NAMESPACE:-easylab}"
BUILDKIT="${BUILDKIT_ADDR:-tcp://buildkitd.temp.svc.cluster.local:1234}"
FORGEJO_USER="${FORGEJO_USER:-root}"
FORGEJO_PASS="${FORGEJO_PASS:-devpassword}"
EASYLAB="${EASYLAB:-http://easylab.temp.svc.cluster.local}"
APT_PROXY="${APT_PROXY:-}"

VARIANT="${VARIANT:-bookworm}"
case "$VARIANT" in
  trixie)   DEF_TAG="trixie"; DEF_SLIM="${REGISTRY}/root/debian:trixie-slim" ;;
  bookworm) DEF_TAG="latest"; DEF_SLIM="${REGISTRY}/root/debian:bookworm-slim" ;;
  *) echo "unknown VARIANT=${VARIANT} (bookworm|trixie)"; exit 1 ;;
esac
TAG="${2:-$DEF_TAG}"
BASE_SLIM="${BASE_SLIM:-$DEF_SLIM}"
BASE_TAG="${BASE_TAG:-$DEF_TAG}"
BASE_IMAGE="${BASE_IMAGE:-${REGISTRY}/${NAMESPACE}/toolchain-base:${BASE_TAG}}"

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

echo "== build ${NAME}:${TAG} on buildkit ${BUILDKIT} (variant=${VARIANT}) =="
buildctl --addr "${BUILDKIT}" build \
  --frontend dockerfile.v0 \
  --local "context=${CTX}" \
  --local "dockerfile=${CTX}" \
  --opt "filename=$(basename "${DOCKERFILE}")" \
  --opt "build-arg:EASYLAB=${EASYLAB}" \
  --opt "build-arg:BASE_IMAGE=${BASE_IMAGE}" \
  --opt "build-arg:BASE_SLIM=${BASE_SLIM}" \
  --opt "build-arg:APT_PROXY=${APT_PROXY}" \
  --output "type=docker,name=${NAMESPACE}/${NAME}:${TAG},dest=${WORK}/image.tar" \
  --progress plain

echo "== push ${DEST} =="
skopeo copy --dest-creds "${FORGEJO_USER}:${FORGEJO_PASS}" --dest-tls-verify=false \
  "docker-archive:${WORK}/image.tar:${NAMESPACE}/${NAME}:${TAG}" "docker://${DEST}"
skopeo inspect --creds "${FORGEJO_USER}:${FORGEJO_PASS}" --tls-verify=false "docker://${DEST}" >/dev/null \
  && echo "OK ${DEST}"
