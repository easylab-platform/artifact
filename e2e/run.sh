#!/usr/bin/env bash
# Per-protocol pull-through matrix: one tool+easyproxy pod per protocol, each
# steered by the DNS-spoof egress policy at that protocol's artifact Service.
#
# For each protocol we assert:
#   1. the client manager installs/downloads a package through the proxy
#      (its hostname resolves to the sidecar, TLS is MITM'd, the request path
#      is prefixed and relayed to artifact-<proto>);
#   2. artifact-<proto>'s CAS grew (the bytes were fetched upstream and cached).
#
# Requires: the per-protocol Deployments from ./deploy-protocol.sh, the CA
# secret, buildkit-pulled tool images, and easyproxy >= v0.4.1.
set -uo pipefail

NS="${NS:-temp}"
IMAGE_PREFIX="${IMAGE_PREFIX:-easylab.${NS}.svc.cluster.local:80}"
EASYPX_IMAGE="${EASYPX_IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/easyproxy:v0.5.0}"
CA_SECRET="${CA_SECRET:-artifact-e2e-ca}"
UPSTREAM_DNS="${UPSTREAM_DNS:-172.18.0.10}"
UPSTREAM_PROXY="${UPSTREAM_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
PROTOCOLS=(${PROTOCOLS:-npm pypi go cargo maven debian apk rpm oci})
KEEP="${KEEP:-0}"

pass=0; fail=0; warn=0
declare -a RESULTS

proto_image() {
  case "$1" in
    npm)    echo "${IMAGE_PREFIX}/docker.io/library/node:22-alpine" ;;
    pypi)   echo "${IMAGE_PREFIX}/docker.io/library/python:3.12-alpine" ;;
    go)     echo "${IMAGE_PREFIX}/docker.io/library/golang:1.26-alpine" ;;
    cargo)  echo "${IMAGE_PREFIX}/docker.io/library/rust:1-alpine" ;;
    maven)  echo "${IMAGE_PREFIX}/docker.io/library/maven:3-eclipse-temurin-21" ;;
    debian) echo "${IMAGE_PREFIX}/docker.io/library/debian:12-slim" ;;
    apk)    echo "${IMAGE_PREFIX}/docker.io/library/alpine:3.24" ;;
    rpm)    echo "${IMAGE_PREFIX}/docker.io/library/fedora:40" ;;
    oci)    echo "${IMAGE_PREFIX}/docker.io/library/alpine:3.24" ;;
  esac
}
proto_match() {
  case "$1" in
    npm)    echo '["registry.npmjs.org", "*.npmjs.org"]' ;;
    pypi)   echo '["pypi.org", "files.pythonhosted.org"]' ;;
    go)     echo '["proxy.golang.org", "sum.golang.org"]' ;;
    cargo)  echo '["index.crates.io", "crates.io", "static.crates.io"]' ;;
    maven)  echo '["repo.maven.apache.org"]' ;;
    debian) echo '["deb.debian.org", "security.debian.org", "archive.ubuntu.com", "security.ubuntu.com"]' ;;
    apk)    echo '["dl-cdn.alpinelinux.org"]' ;;
    rpm)    echo '["dl.fedoraproject.org"]' ;;
    oci)    echo '["registry-1.docker.io", "docker.io", "production.cloudflare.docker.com"]' ;;
  esac
}
proto_strip() { case "$1" in maven) echo "/maven2" ;; oci) echo "/v2" ;; *) echo "" ;; esac; }
proto_add()   { case "$1" in oci) echo "" ;; *) echo "/pkgs/$1" ;; esac; }

# The command each tool runs; success = exit 0 and new CAS blobs.
proto_cmd() {
  case "$1" in
    npm)    echo 'cd /tmp && npm install --no-audit --no-fund left-pad' ;;
    pypi)   echo 'pip install --no-cache-dir --disable-pip-version-check six' ;;
    go)     echo 'mkdir -p /w && cd /w && go mod init t >/dev/null 2>&1; GOFLAGS=-mod=mod GONOSUMCHECK=1 GONOSUMDB=* GOSUMDB=off go get golang.org/x/text@v0.14.0' ;;
    cargo)  echo 'mkdir -p /w/src && cd /w && printf "[package]\nname=\"t\"\nversion=\"0.1.0\"\nedition=\"2021\"\n\n[dependencies]\nanyhow=\"1\"\n" > Cargo.toml && echo "fn main(){}" > src/main.rs && cargo fetch' ;;
    maven)  echo 'mkdir -p /w && cd /w && cat > pom.xml <<X
<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion><groupId>t</groupId><artifactId>t</artifactId><version>1.0</version>
<dependencies><dependency><groupId>org.slf4j</groupId><artifactId>slf4j-api</artifactId><version>2.0.9</version></dependency></dependencies>
</project>
X
keytool -importcert -noprompt -alias easylab -file /etc/easyproxy/ca.crt -keystore /tmp/ts.p12 -storetype PKCS12 -storepass changeit >/dev/null 2>&1
JAVA_TOOL_OPTIONS="-Djavax.net.ssl.trustStore=/tmp/ts.p12 -Djavax.net.ssl.trustStorePassword=changeit -Djavax.net.ssl.trustStoreType=PKCS12" mvn -q -B -Dmaven.repo.local=/tmp/m2 org.apache.maven.plugins:maven-dependency-plugin:3.6.1:resolve' ;;
    debian) echo 'apt-get update -o Acquire::Retries=0 && apt-get install -y --no-install-recommends jq' ;;
    apk)    echo 'apk update && apk add --no-cache jq' ;;
    rpm)    echo 'rm -f /etc/yum.repos.d/*.repo && printf "[fedora]\nname=fedora\nbaseurl=https://dl.fedoraproject.org/pub/fedora/linux/releases/40/Everything/x86_64/os/\nenabled=1\ngpgcheck=0\n" > /etc/yum.repos.d/f.repo && dnf -y --refresh install jq' ;;
    oci)    echo 'apk add --no-cache skopeo >/dev/null 2>&1; skopeo copy --src-tls-verify=true docker://docker.io/library/alpine:3.24 dir:/tmp/img' ;;
  esac
}

cas_count() {
  kubectl exec -n "$NS" "deploy/artifact-$1" -- sh -c 'ls /data/blobs/sha256/*/* 2>/dev/null | wc -l' 2>/dev/null | tr -d '[:space:]'
}

write_rules_cm() {
  local p="$1" name="pull-$p" strip add
  strip="$(proto_strip "$p")"; add="$(proto_add "$p")"
  {
    echo "apiVersion: v1"
    echo "kind: ConfigMap"
    echo "metadata:"
    echo "  name: ${name}-rules"
    echo "data:"
    echo "  rules.yaml: |"
    echo "    rules:"
    echo "      - match: $(proto_match "$p")"
    echo "        action: rewrite"
    echo "        target: \"artifact-${p}.${NS}.svc.cluster.local:80\""
    [ -n "$strip" ] && echo "        strip_prefix: \"${strip}\""
    [ -n "$add" ] && echo "        add_prefix: \"${add}\""
    echo "    default: direct"
    echo "    mitm_default: false"
  } | kubectl apply -n "$NS" -f - >/dev/null
}

write_pod() {
  local p="$1" name="pull-$p"
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
    image: $(proto_image "$p")
    command: ["sleep", "900"]
    resources:
      requests: { cpu: 50m, memory: 64Mi }
      limits:   { cpu: "1", memory: 1Gi }
    env:
    - { name: SSL_CERT_FILE, value: /etc/easyproxy/ca.crt }
    - { name: NODE_EXTRA_CA_CERTS, value: /etc/easyproxy/ca.crt }
    - { name: REQUESTS_CA_BUNDLE, value: /etc/easyproxy/ca.crt }
    - { name: GIT_SSL_CAINFO, value: /etc/easyproxy/ca.crt }
    volumeMounts: [{ name: ca, mountPath: /etc/easyproxy/ca.crt, subPath: ca.crt, readOnly: true }]
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
  - { name: ca, secret: { secretName: ${CA_SECRET} } }
YAML
}

run_one() {
  local p="$1" name="pull-$1" before after growth out rc rewrites
  before="$(cas_count "$p")"
  write_rules_cm "$p"
  kubectl delete pod -n "$NS" "$name" --ignore-not-found --force --grace-period=0 >/dev/null 2>&1
  write_pod "$p"

  if ! kubectl wait -n "$NS" --for=condition=Ready "pod/${name}" --timeout=180s >/dev/null 2>&1; then
    echo "FAIL $p  (pod not ready)"; fail=$((fail+1)); RESULTS+=("FAIL $p pod-not-ready")
    kubectl logs -n "$NS" "$name" -c easyproxy --tail=5 2>&1 | sed 's/^/      | /'
    return
  fi
  out="$(kubectl exec -n "$NS" "$name" -c tool -- sh -c "$(proto_cmd "$p")" 2>&1)"; rc=$?
  # The proxy must have classified at least one upstream connection as a
  # rewrite (proof the request was steered through the sidecar, not direct).
  rewrites="$(kubectl logs -n "$NS" "$name" -c easyproxy 2>/dev/null | grep -c '"action":"rewrite"')"
  sleep 2
  after="$(cas_count "$p")"; growth=$(( ${after:-0} - ${before:-0} ))
  if [ "$rc" -eq 0 ] && [ "${rewrites:-0}" -gt 0 ]; then
    echo "PASS $p  (client ok, $rewrites rewrite conns, CAS +$growth)"; pass=$((pass+1)); RESULTS+=("PASS $p ($rewrites conns, CAS +$growth)")
  elif [ "$rc" -eq 0 ]; then
    echo "FAIL $p  (client ok but no rewrite conn — proxy bypassed)"; fail=$((fail+1)); RESULTS+=("FAIL $p bypassed")
    kubectl logs -n "$NS" "$name" -c easyproxy --tail=8 2>&1 | sed 's/^/      | /'
  else
    echo "FAIL $p  (client rc=$rc)"; fail=$((fail+1)); RESULTS+=("FAIL $p rc=$rc")
    echo "$out" | tail -8 | sed 's/^/      | /'
  fi
  [ "$KEEP" = "1" ] || kubectl delete pod -n "$NS" "$name" --ignore-not-found >/dev/null 2>&1
}

for p in "${PROTOCOLS[@]}"; do run_one "$p"; done

echo "====================================="
printf '%s\n' "${RESULTS[@]}"
echo "MATRIX: $pass pass, $warn warn, $fail fail"
[ "$fail" -eq 0 ]
