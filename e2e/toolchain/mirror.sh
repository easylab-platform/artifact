#!/usr/bin/env bash
# Mirror every toolchain artifact into easylab's generic store so the image
# build can fetch them from inside the cluster (no egress proxy needed at build
# time). Run from a host that can reach easylab and the public internet (the
# dev box uses mihomo).
#
# Each artifact is cached at /artifacts/generic/<name>/<version>/<filename>; the
# mirror is idempotent (skips artifacts already present with the same size).
set -euo pipefail

EASYLAB="${EASYLAB:-http://172.18.199.215}"      # easylab ClusterIP (or svc DNS)
TOKEN="${ARTIFACT_TOKEN:-devtoken}"
PROXY="${MIRROR_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MANIFEST="${MANIFEST:-${HERE}/manifest.txt}"

auth=(-H "Authorization: Bearer ${TOKEN}")
ok=0; skip=0; fail=0

while IFS='|' read -r name ver file url; do
  [ -z "$name" ] && continue
  dest="${EASYLAB}/artifacts/generic/${name}/${ver}/${file}"
  # Already mirrored? (HEAD returns 200 when the blob is present.)
  if curl -fsS -o /dev/null -I "${auth[@]}" "$dest" 2>/dev/null; then
    echo "skip  ${name}/${ver}/${file}"
    skip=$((skip+1)); continue
  fi
  echo "fetch ${name}/${ver}/${file}"
  tmp="$(mktemp)"
  case "$url" in
    LOCAL:*)
      src="${url#LOCAL:}"
      cp "$src" "$tmp"
      dl_ok=0
      ;;
    *)
      HTTPS_PROXY="$PROXY" HTTP_PROXY="$PROXY" curl -fsSL --retry 3 --retry-delay 2 -o "$tmp" "$url"
      dl_ok=$?
      ;;
  esac
  if [ "${dl_ok:-1}" -eq 0 ]; then
    sz=$(wc -c <"$tmp")
    if curl -fsS -o /dev/null -X PUT "${auth[@]}" --data-binary "@$tmp" "$dest"; then
      echo "  -> mirrored (${sz} bytes)"
      ok=$((ok+1))
    else
      echo "  -> PUT FAILED"; fail=$((fail+1))
    fi
  else
    echo "  -> DOWNLOAD FAILED"; fail=$((fail+1))
  fi
  rm -f "$tmp"
done < "$MANIFEST"

echo "mirror: $ok uploaded, $skip skipped, $fail failed"
[ "$fail" -eq 0 ]
