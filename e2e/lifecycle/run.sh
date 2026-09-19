#!/usr/bin/env bash
# Full package-lifecycle matrix: for each protocol, one tool+easysidecar pod
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
#   CAPTURE=1 ./run.sh                # exercise the all-port capture mode
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NS="${NS:-temp}"
TOOL_IMAGE_PREFIX="${TOOL_IMAGE_PREFIX:-forgejo.develop.10.199.64.20.nip.io/easylab/tool}"
TOOL_TAG="${TOOL_TAG:-latest}"
EASYSIDECAR_IMAGE="${EASYSIDECAR_IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/easysidecar:v0.13.0}"
CA_SECRET="${CA_SECRET:-artifact-e2e-ca}"
UPSTREAM_DNS="${UPSTREAM_DNS:-172.18.0.10}"
UPSTREAM_PROXY="${UPSTREAM_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
# Real-client protocols first; extend the list as stage scripts are added.
PROTOCOLS=(${PROTOCOLS:-npm pypi cargo maven nuget rubygems pub conan oci hex swift helm composer go debian apk rpm conda})
KEEP="${KEEP:-0}"
V1="${V1:-1.0.0}"
V2="${V2:-2.0.0}"
STAGES=(publish public private upgrade delete)
# JOBS protocols run concurrently (each has its own pod). JOBS=1 is serial.
JOBS="${JOBS:-6}"
# CAPTURE=1 runs the lifecycle through the privileged all-port capture mode.
CAPTURE="${CAPTURE:-0}"

source "${HERE}/../rules.sh"
source "${HERE}/../parallel.sh"

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
  kubectl logs -n "$NS" "$1" -c easysidecar 2>/dev/null | grep -c '"action":"rewrite"'
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
  # CAPTURE=1 exercises the privileged all-port capture mode (init container
  # redirects TCP; SO_ORIGINAL_DST recovers the destination) with the DNS
  # assist, instead of DNS-spoof.
  local init_block="" sidecar_args sidecar_sec=""
  if [ "${CAPTURE:-0}" = "1" ]; then
    init_block=$(cat <<YAML
  initContainers:
  - name: easysidecar-capture-init
    image: ${EASYSIDECAR_IMAGE}
    args: ["--mode=capture","--capture-init","--capture-addr=0.0.0.0:15001"]
    securityContext: { capabilities: { add: ["NET_ADMIN"] } }
    resources:
      requests: { cpu: 20m, memory: 32Mi }
      limits:   { cpu: 200m, memory: 128Mi }
YAML
)
    sidecar_args=$(cat <<YAML
    - --mode=capture
    - --capture-addr=0.0.0.0:15001
    - --capture-dns
    - --spoof-dns-addr=0.0.0.0:53
YAML
)
    sidecar_sec='    securityContext: { capabilities: { add: ["NET_ADMIN"] } }'
  else
    sidecar_args=$(cat <<YAML
    - --mode=proxy
    - --spoof
    - --spoof-dns-addr=0.0.0.0:53
    - --spoof-tls-addr=0.0.0.0:443
    - --spoof-http-addr=0.0.0.0:80
YAML
)
  fi
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
${init_block}
  containers:
  - name: tool
    image: ${R_IMG}
    imagePullPolicy: Always
    command: ["sleep", "3600"]
    resources:
      requests: { cpu: 100m, memory: 256Mi }
      limits:   { cpu: "2", memory: 4Gi }
    env:
    - { name: SSL_CERT_FILE, value: /etc/easysidecar/ca.crt }
    - { name: NODE_EXTRA_CA_CERTS, value: /etc/easysidecar/ca.crt }
    - { name: REQUESTS_CA_BUNDLE, value: /etc/easysidecar/ca.crt }
    - { name: CURL_CA_BUNDLE, value: /etc/easysidecar/ca.crt }
    - { name: GIT_SSL_CAINFO, value: /etc/easysidecar/ca.crt }
    - { name: SSL_CERT_DIR, value: /etc/easysidecar/ca }
    volumeMounts:
    - { name: ca, mountPath: /etc/easysidecar/ca.crt, subPath: ca.crt, readOnly: true }
    - { name: script, mountPath: /scripts, readOnly: true }
  - name: easysidecar
    image: ${EASYSIDECAR_IMAGE}
${sidecar_sec}
    resources:
      requests: { cpu: 50m, memory: 64Mi }
      limits:   { cpu: 500m, memory: 256Mi }
    args:
    - --rules=/etc/easysidecar/rules.yaml
${sidecar_args}
    - --upstream-dns=${UPSTREAM_DNS}
    - --upstream-proxy=${UPSTREAM_PROXY}
    - --ca-cert=/etc/easysidecar/ca/ca.crt
    - --ca-key=/etc/easysidecar/ca/ca.key
    env:
    - name: POD_IP
      valueFrom: { fieldRef: { fieldPath: status.podIP } }
    volumeMounts:
    - { name: rules, mountPath: /etc/easysidecar, readOnly: true }
    - { name: ca, mountPath: /etc/easysidecar/ca, readOnly: true }
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
    sh -c 'cat /etc/easysidecar/ca.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
rm -rf "$HOME" "$WORK"; mkdir -p "$HOME" "$WORK"; sh "/scripts/'"$p"'.sh"'
}

run_one() {
  local p="$1" name="lc-$1" stage ver rc out rewrites stage_result
  local failed=0
  SUFFIX="$(date +%s%N)"
  local pkg; pkg="$(proto_pkg "$p")"

  write_rules_cm lc "$p"
  write_script_cm "$p"
  kubectl delete pod -n "$NS" "$name" --ignore-not-found --force --grace-period=0 >/dev/null 2>&1
  write_pod "$p"
  if ! kubectl wait -n "$NS" --for=condition=Ready "pod/${name}" --timeout=300s >/dev/null 2>&1; then
    echo "FAIL $p (pod not ready)"
    emit_result "$p" "FAIL $p pod-not-ready"
    kubectl logs -n "$NS" "$name" -c easysidecar --tail=5 2>&1 | sed 's/^/      | /'
    return
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
    emit_result "$p" "PASS $p (${rewrites} conns) [${STAGES[*]}]"
  else
    emit_result "$p" "FAIL $p (see above)"
  fi
  [ "$KEEP" = "1" ] || kubectl delete pod -n "$NS" "$name" --ignore-not-found >/dev/null 2>&1
}

run_pool run_one "${PROTOCOLS[@]}"
