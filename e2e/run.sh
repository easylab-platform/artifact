#!/usr/bin/env bash
# Per-protocol pull-through matrix: one tool+easyproxy pod per protocol, each
# steered by the DNS-spoof egress policy at that protocol's artifact Service.
#
# For each protocol we assert:
#   1. the client's package manager installs/downloads a package through the
#      proxy (its upstream hostname resolves to the sidecar, TLS is MITM'd,
#      the request path is prefixed/stripped and relayed to artifact-<proto>);
#   2. the sidecar logged >=1 `rewrite` connection (proof it was not bypassed).
#
# Per-protocol client commands live in ./scripts/<proto>.sh and are mounted
# into the tool container at /scripts.
#
# Usage:
#   ./run.sh                       # all protocols (default list)
#   PROTOCOLS="nuget rubygems" ./run.sh
#   KEEP=1 PROTOCOLS=helm ./run.sh # keep the pod for debugging
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NS="${NS:-temp}"
TOOL_IMAGE_PREFIX="${TOOL_IMAGE_PREFIX:-forgejo.develop.10.199.64.20.nip.io/easylab/tool}"
TOOL_TAG="${TOOL_TAG:-latest}"
EASYPX_IMAGE="${EASYPX_IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/easyproxy:v0.5.3}"
CA_SECRET="${CA_SECRET:-artifact-e2e-ca}"
UPSTREAM_DNS="${UPSTREAM_DNS:-172.18.0.10}"
UPSTREAM_PROXY="${UPSTREAM_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
# Each protocol uses its dedicated client image (toolchain/e2e: one image per
# protocol, built from official/prebuilt tarballs under /opt).
PROTOCOLS=(${PROTOCOLS:-npm pypi go cargo maven nuget rubygems composer hex pub helm conan swift conda nix huggingface protobuf debian apk rpm oci})
KEEP="${KEEP:-0}"

source "${HERE}/rules.sh"

pass=0; fail=0
declare -a RESULTS

# Per-protocol columns (pipe separated, values must not contain "|"):
#   match json | strip_prefix | add_prefix
# The tool image is always ${TOOL_IMAGE_PREFIX}-<proto>:${TOOL_TAG}; the client
# command is ./scripts/<proto>.sh.
#
# UNIFIED_SVC=<name> points every sidecar rule at one Service (see
# deploy-unified.sh) instead of the per-protocol artifact-<p>; the matrix then
# proves the unified-mount topology is transparent.
cas_count() {
  local dep="deploy/artifact-$1"
  [ -n "${UNIFIED_SVC:-}" ] && dep="deploy/${UNIFIED_SVC}"
  kubectl exec -n "$NS" "$dep" -- sh -c 'ls /data/blobs/sha256/*/* 2>/dev/null | wc -l' 2>/dev/null | tr -d '[:space:]'
}

# rewrite_count prints how many rewrite connections the sidecar has logged for
# the pod so far.
rewrite_count() {
  kubectl logs -n "$NS" "$1" -c easyproxy 2>/dev/null | grep -c '"action":"rewrite"'
}

# await_rewrites polls until the sidecar has logged >=1 rewrite result for the
# pod (or the timeout elapses). The sidecar writes its log line when a relayed
# connection closes, and clients that keep sockets alive (apt, cargo, maven, …)
# may close them slightly after the client command returns — so a single read
# races. max_wait/interval are seconds.
await_rewrites() {
  local name="$1" max_wait="${2:-20}" interval="${3:-1}" waited=0 n=0
  while [ "$waited" -lt "$max_wait" ]; do
    n="$(rewrite_count "$name")"
    [ "${n:-0}" -gt 0 ] && { echo "$n"; return 0; }
    sleep "$interval"; waited=$((waited + interval))
  done
  echo "${n:-0}"; return 1
}

write_script_cm() {
  local p="$1" name="pull-$1"
  kubectl create configmap "${name}-script" -n "$NS" \
    --from-file="${p}.sh=${HERE}/scripts/${p}.sh" \
    --dry-run=client -o yaml | kubectl apply -n "$NS" -f - >/dev/null
}

write_pod() {
  local p="$1" name="pull-$1"
  parse_row "$p"
  cat <<YAML | kubectl apply -n "$NS" -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: { name: ${name}, labels: { app: pull } }
spec:
  dnsPolicy: None
  dnsConfig:
    nameservers: ["127.0.0.1"]
    searches: ["${NS}.svc.cluster.local", "svc.cluster.local", "cluster.local"]
    options: [{ name: ndots, value: "5" }]
  containers:
  - name: tool
    image: ${R_IMG}
    # Always pull: the tool images are re-pushed under the same tag as the
    # matrix evolves, and IfNotPresent would silently run a stale cached layer.
    imagePullPolicy: Always
    command: ["sleep", "1800"]
    resources:
      requests: { cpu: 100m, memory: 256Mi }
      limits:   { cpu: "2", memory: 4Gi }
    env:
    - { name: SSL_CERT_FILE, value: /etc/easyproxy/ca.crt }
    - { name: NODE_EXTRA_CA_CERTS, value: /etc/easyproxy/ca.crt }
    - { name: REQUESTS_CA_BUNDLE, value: /etc/easyproxy/ca.crt }
    - { name: CURL_CA_BUNDLE, value: /etc/easyproxy/ca.crt }
    - { name: GIT_SSL_CAINFO, value: /etc/easyproxy/ca.crt }
    - { name: SSL_CERT_DIR, value: /etc/easyproxy/ca }
    volumeMounts:
    - { name: ca, mountPath: /etc/easyproxy/ca.crt, subPath: ca.crt, readOnly: true }
    - { name: script, mountPath: /scripts, readOnly: true }
  - name: easyproxy
    image: ${EASYPX_IMAGE}
    resources:
      requests: { cpu: 50m, memory: 64Mi }
      limits:   { cpu: 500m, memory: 256Mi }
    args:
    - --mode=proxy
    - --rules=/etc/easyproxy/rules.yaml
    - --spoof
    - --spoof-dns-addr=0.0.0.0:53
    - --spoof-tls-addr=0.0.0.0:443
    - --spoof-http-addr=0.0.0.0:80
    - --upstream-dns=${UPSTREAM_DNS}
    - --upstream-proxy=${UPSTREAM_PROXY}
    - --ca-cert=/etc/easyproxy/ca/ca.crt
    - --ca-key=/etc/easyproxy/ca/ca.key
    env:
    - name: POD_IP
      valueFrom: { fieldRef: { fieldPath: status.podIP } }
    volumeMounts:
    - { name: rules, mountPath: /etc/easyproxy, readOnly: true }
    - { name: ca, mountPath: /etc/easyproxy/ca, readOnly: true }
  volumes:
  - { name: rules, configMap: { name: ${name}-rules } }
  - { name: script, configMap: { name: ${name}-script } }
  - { name: ca, secret: { secretName: ${CA_SECRET} } }
YAML
}

run_one() {
  local p="$1" name="pull-$1" before after growth out rc rewrites
  before="$(cas_count "$p")"
  write_rules_cm pull "$p"
  write_script_cm "$p"
  kubectl delete pod -n "$NS" "$name" --ignore-not-found --force --grace-period=0 >/dev/null 2>&1
  write_pod "$p"

  if ! kubectl wait -n "$NS" --for=condition=Ready "pod/${name}" --timeout=300s >/dev/null 2>&1; then
    echo "FAIL $p  (pod not ready)"; fail=$((fail+1)); RESULTS+=("FAIL $p pod-not-ready")
    kubectl logs -n "$NS" "$name" -c easyproxy --tail=5 2>&1 | sed 's/^/      | /'
    return
  fi
  out="$(kubectl exec -n "$NS" "$name" -c tool -- timeout 600 sh "/scripts/${p}.sh" 2>&1)"; rc=$?
  # The sidecar logs a rewrite entry when the relayed connection closes; wait
  # for it (bounded) rather than racing a single read.
  rewrites="$(await_rewrites "$name" 20 1 || true)"
  after="$(cas_count "$p")"; growth=$(( ${after:-0} - ${before:-0} ))
  if [ "$rc" -eq 0 ] && [ "${rewrites:-0}" -gt 0 ]; then
    echo "PASS $p  ($rewrites rewrite conns, CAS +$growth)"; pass=$((pass+1)); RESULTS+=("PASS $p ($rewrites conns, CAS +$growth)")
  elif [ "$rc" -eq 0 ]; then
    echo "FAIL $p  (client ok but no rewrite conn — bypassed)"; fail=$((fail+1)); RESULTS+=("FAIL $p bypassed")
    kubectl logs -n "$NS" "$name" -c easyproxy --tail=8 2>&1 | sed 's/^/      | /'
  else
    echo "FAIL $p  (client rc=$rc)"; fail=$((fail+1)); RESULTS+=("FAIL $p rc=$rc")
    echo "$out" | tail -12 | sed 's/^/      | /'
  fi
  [ "$KEEP" = "1" ] || kubectl delete pod -n "$NS" "$name" --ignore-not-found >/dev/null 2>&1
}

for p in "${PROTOCOLS[@]}"; do run_one "$p"; done

echo "====================================="
printf '%s\n' "${RESULTS[@]}"
echo "MATRIX: $pass pass, $fail fail"
[ "$fail" -eq 0 ]
