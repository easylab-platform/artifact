#!/usr/bin/env bash
# One-command regression for the artifact registry: build a single image and
# exercise it BOTH ways, plus the auxiliary matrices.
#
#   ./test-all.sh                 # unified + per-protocol + lifecycle + search
#   ./test-all.sh unified         # one service, all protocols concurrent
#   ./test-all.sh perproto        # one service per protocol (isolation)
#   ./test-all.sh lifecycle       # publish/upgrade/delete per protocol
#   ./test-all.sh search          # search/aux endpoints
#
# The unified topology is the production shape (easylab runs one instance for
# every protocol); the per-protocol topology isolates each adapter so a failure
# cannot be masked by a sibling. Both assert the client's package manager
# installs through the sidecar AND the CAS grew, so "fetched once" is real.
#
# Env:
#   NS                 namespace (default temp)
#   IMAGE              artifact image (default the pinned release below)
#   EASYSIDECAR_IMAGE  sidecar image
#   JOBS               protocols exercised concurrently (default 8)
#   PROTOCOLS          override the matrix (space separated)
#   SUITES             override which suites run (space separated)
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NS="${NS:-temp}"
IMAGE="${IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/artifact:v0.23.0}"
export EASYSIDECAR_IMAGE="${EASYSIDECAR_IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/easysidecar:v0.13.0}"
export NS IMAGE JOBS="${JOBS:-8}"

SUITES="${SUITES:-${*:-unified perproto lifecycle search}}"

run_unified() {
  echo "########## unified: ONE service, all protocols concurrent ##########"
  NS="$NS" IMAGE="$IMAGE" "${HERE}/deploy-unified.sh" >/dev/null
  kubectl -n "$NS" rollout status deploy/artifact-unified --timeout=180s >/dev/null
  UNIFIED_SVC=artifact-unified NS="$NS" JOBS="$JOBS" "${HERE}/run.sh"
}

run_perproto() {
  echo "########## per-protocol: one service per protocol (isolation) ##########"
  NS="$NS" IMAGE="$IMAGE" "${HERE}/deploy-protocol.sh" >/dev/null
  NS="$NS" JOBS="$JOBS" "${HERE}/run.sh"
}

run_lifecycle() {
  echo "########## lifecycle: publish -> public -> private -> upgrade -> delete ##########"
  NS="$NS" IMAGE="$IMAGE" "${HERE}/lifecycle/run.sh"
}

run_search() {
  echo "########## search/aux matrix ##########"
  NS="$NS" "${HERE}/run-search.sh"
}

rc=0
for s in $SUITES; do
  case "$s" in
    unified)   run_unified   || rc=1 ;;
    perproto)  run_perproto  || rc=1 ;;
    lifecycle) run_lifecycle || rc=1 ;;
    search)    run_search    || rc=1 ;;
    *) echo "unknown suite: $s (unified|perproto|lifecycle|search)"; rc=1 ;;
  esac
done
exit "$rc"
