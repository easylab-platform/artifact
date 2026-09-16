#!/usr/bin/env bash
# Full package-lifecycle matrix: for each protocol, one tool+easyproxy pod
# walks five stages with a REAL client —
#   1 publish   A publishes a uniquely-named private package (v1)
#   2 public    B installs a well-known PUBLIC package (pull-through still
#               works alongside the private content)
#   3 private   B (fresh caches) installs A's private package v1
#   4 upgrade   A publishes v2, B consumes it; both versions visible
#   5 delete    the package is deleted (unpublish / unlist / yank — whatever
#               the protocol supports) and B's install now fails
# A/B are separate processes with isolated HOMEs/caches in the same pod.
#
# The client talks to the REAL upstream hostnames; the spoofing sidecar maps
# them onto artifact-<proto> (deployed in origin self-base mode), so publish
# and pull are transparent to an unmodified client.
#
# Usage:
#   ./run.sh                          # all lifecycle protocols
#   PROTOCOLS="npm oci" ./run.sh
#   KEEP=1 PROTOCOLS=pypi ./run.sh    # keep the pod for debugging
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NS="${NS:-temp}"
TOOL_IMAGE_PREFIX="${TOOL_IMAGE_PREFIX:-forgejo.develop.10.199.64.20.nip.io/easylab/tool}"
TOOL_TAG="${TOOL_TAG:-latest}"
EASYPX_IMAGE="${EASYPX_IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/easyproxy:v0.5.1}"
CA_SECRET="${CA_SECRET:-artifact-e2e-ca}"
UPSTREAM_DNS="${UPSTREAM_DNS:-172.18.0.10}"
UPSTREAM_PROXY="${UPSTREAM_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
# Real-client protocols first; extend the list as stage scripts are added.
PROTOCOLS=(${PROTOCOLS:-npm pypi cargo maven nuget rubygems pub conan oci hex swift helm composer go debian apk rpm conda})
KEEP="${KEEP:-0}"
V1="${V1:-1.0.0}"
V2="${V2:-2.0.0}"
STAGES=(publish public private upgrade delete)

source "${HERE}/../rules.sh"

# Per-protocol fully-qualified package name for run SUFFIX.
proto_pkg() {
  case "$1" in
    npm)      echo "@lc${SUFFIX}/probe" ;;
    pub)      echo "lc_probe_${SUFFIX}" ;;
    nuget)    echo "Lc.Probe.${SUFFIX}" ;;
    *)        echo "lc-probe-${SUFFIX}" ;;
  esac
}

rewrite_count() {
  kubectl logs -n "$NS" "$1" -c easyproxy 2>/dev/null | grep -c '"action":"rewrite"'
}

write_script_cm() {
  local p="$1" name="lc-$1"
  kubectl create configmap "${name}-script" -n "$NS" \
    --from-file="${p}.sh=${HERE}/scripts/${p}.sh" \
    --dry-run=client -o yaml | kubectl apply -n "$NS" -f - >/dev/null
}

write_pod() {
  local p="$1" name="lc-$1"
  parse_row "$p"
  cat <<YAML | kubectl apply -n "$NS" -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: { name: ${name}, labels: { app: lc } }
spec:
  dnsPolicy: None
  dnsConfig:
    nameservers: ["127.0.0.1"]
    searches: ["${NS}.svc.cluster.local", "svc.cluster.local", "cluster.local"]
    options: [{ name: ndots, value: "5" }]
  containers:
  - name: tool
    image: ${R_IMG}
    imagePullPolicy: Always
    command: ["sleep", "3600"]
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

# run_stage <pod> <proto> <stage> <ver>: fresh HOME/WORK per stage so every
# stage is a cold client. Prints the exec output, returns its rc.
run_stage() {
  local name="$1" p="$2" stage="$3" ver="$4"
  kubectl exec -n "$NS" "$name" -c tool -- env \
    STAGE="$stage" SUFFIX="$SUFFIX" NAME="$(proto_pkg "$p")" \
    V1="$V1" V2="$V2" VER="$ver" \
    HOME="/tmp/lc-home-${stage}" WORK="/tmp/lc-work-${stage}" \
    sh -c 'cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
rm -rf "$HOME" "$WORK"; mkdir -p "$HOME" "$WORK"; sh "/scripts/'"$p"'.sh"'
}

run_one() {
  local p="$1" name="lc-$1" stage ver rc out rewrites stage_result
  local failed=0
  SUFFIX="$(date +%s)"
  local pkg; pkg="$(proto_pkg "$p")"

  write_rules_cm lc "$p"
  write_script_cm "$p"
  kubectl delete pod -n "$NS" "$name" --ignore-not-found --force --grace-period=0 >/dev/null 2>&1
  write_pod "$p"
  if ! kubectl wait -n "$NS" --for=condition=Ready "pod/${name}" --timeout=300s >/dev/null 2>&1; then
    echo "FAIL $p (pod not ready)"
    kubectl logs -n "$NS" "$name" -c easyproxy --tail=5 2>&1 | sed 's/^/      | /'
    RESULTS+=("FAIL $p pod-not-ready"); fail=$((fail+1)); return
  fi

  printf '%-9s %s\n' "$p" "$pkg"
  for stage in "${STAGES[@]}"; do
    # publish publishes v1; upgrade publishes v2 then consumes it; the other
    # stages consume/verify.
    ver="$V1"; [ "$stage" = "upgrade" ] && ver="$V2"
    out="$(run_stage "$name" "$p" "$stage" "$ver" 2>&1)"; rc=$?
    if [ "$rc" -eq 0 ]; then
      stage_result="PASS"; echo "  PASS ${stage}"
    else
      stage_result="FAIL"; failed=1; echo "  FAIL ${stage} (rc=$rc)"
      printf '%s\n' "$out" | tail -8 | sed 's/^/      | /'
      # A failed stage invalidates the rest of the chain — best-effort remove
      # the package so the next run starts clean, then stop this protocol.
      if [ "$stage" != "delete" ]; then
        run_stage "$name" "$p" delete "$V2" >/dev/null 2>&1 || true
      fi
      break
    fi
  done

  rewrites="$(rewrite_count "$name")"
  if [ "$failed" -eq 0 ] && [ "${rewrites:-0}" -eq 0 ]; then
    echo "  FAIL bypass (no rewrite connections)"; failed=1
  fi
  if [ "$failed" -eq 0 ]; then
    pass=$((pass+1)); RESULTS+=("PASS $p (${rewrites} conns) [${STAGES[*]}]")
  else
    fail=$((fail+1)); RESULTS+=("FAIL $p (see above)")
  fi
  [ "$KEEP" = "1" ] || kubectl delete pod -n "$NS" "$name" --ignore-not-found >/dev/null 2>&1
}

pass=0; fail=0
declare -a RESULTS
for p in "${PROTOCOLS[@]}"; do run_one "$p"; done

echo "====================================="
printf '%s\n' "${RESULTS[@]}"
echo "LIFECYCLE: $pass pass, $fail fail"
[ "$fail" -eq 0 ]
