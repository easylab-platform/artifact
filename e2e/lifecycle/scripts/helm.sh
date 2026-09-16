#!/bin/sh
# helm lifecycle: cm-push (chartmuseum API) / helm pull public+private /
# upgrade / delete via DELETE /api/charts/<name>/<version>.
set -e
cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
export HOME="/tmp/lc-home-${STAGE}"
# The cm-push plugin is baked into the tool image at /root/.local/share/helm.
export HELM_PLUGINS=/root/.local/share/helm/plugins
mkdir -p "$HOME"

REPO=https://charts.helm.sh

make_chart() { # $1 = version
  rm -rf "$WORK/chart"
  mkdir -p "$WORK/chart/templates"
  cd "$WORK/chart"
  cat > Chart.yaml <<X
apiVersion: v2
name: lc-probe-${SUFFIX}
version: $1
description: lc probe
X
  cat > templates/cm.yaml <<X
apiVersion: v1
kind: ConfigMap
metadata:
  name: lc-probe
data:
  version: "$1"
X
  helm package . >/dev/null 2>&1
}

case "$STAGE" in
publish)
  make_chart "$V1"
  cd "$WORK/chart"
  ok=0
  for _ in 1 2 3; do
    if helm cm-push "lc-probe-${SUFFIX}-${V1}.tgz" "$REPO" 2>&1 | grep -qi "done"; then ok=1; break; fi
    sleep 2
  done
  [ "$ok" = "1" ] || { echo "helm: push failed"; exit 1; }
  echo "helm: pushed lc-probe-${SUFFIX}@$V1"
  ;;
public)
  helm repo add easylab "$REPO" >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1
  helm pull easylab/redis -d "$WORK" >/dev/null 2>&1
  cd "$WORK"
  helm template easy redis-*.tgz >/dev/null
  echo "helm: public redis pulled + rendered"
  ;;
private)
  helm repo add easylab "$REPO" >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1
  helm search repo "easylab/lc-probe-${SUFFIX}" | grep -q "$V1" \
    || { echo "helm: $V1 not in index"; exit 1; }
  helm pull "easylab/lc-probe-${SUFFIX}" --version "$V1" -d "$WORK" >/dev/null
  cd "$WORK"
  helm template probe lc-probe-${SUFFIX}-$V1.tgz | grep -q "version: \"$V1\"" \
    || { echo "helm: template mismatch"; exit 1; }
  echo "helm: private lc-probe-${SUFFIX}@$V1 pulled + rendered"
  ;;
upgrade)
  make_chart "$V2"
  cd "$WORK/chart"
  ok=0
  for _ in 1 2 3; do
    if helm cm-push "lc-probe-${SUFFIX}-${V2}.tgz" "$REPO" 2>&1 | grep -qi "done"; then ok=1; break; fi
    sleep 2
  done
  [ "$ok" = "1" ] || { echo "helm: push $V2 failed"; exit 1; }
  helm repo add easylab "$REPO" >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1
  helm search repo "easylab/lc-probe-${SUFFIX}" --versions | grep -q "$V1" \
    || { echo "helm: $V1 dropped from index"; exit 1; }
  helm search repo "easylab/lc-probe-${SUFFIX}" --versions | grep -q "$V2" \
    || { echo "helm: $V2 missing from index"; exit 1; }
  echo "helm: upgraded to $V2, both listed"
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$REPO/api/charts/lc-probe-${SUFFIX}/$V2")"
  [ "$code" = "200" ] || { echo "helm: delete rc=$code"; exit 1; }
  helm repo add easylab "$REPO" >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1
  if helm search repo "easylab/lc-probe-${SUFFIX}" --versions | grep -q "$V2"; then
    echo "helm: $V2 still in index after delete"; exit 1
  fi
  echo "helm: deleted lc-probe-${SUFFIX}@$V2"
  ;;
esac
