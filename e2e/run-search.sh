#!/usr/bin/env bash
# Search/aux matrix: for each protocol, drive the client's SEARCH (or other
# auxiliary) endpoints through the spoofed sidecar and assert useful results.
# This complements run.sh (which only exercises install/download): the goal is
# that an uncached public query returns real upstream results, merged with any
# locally-published packages.
#
# Usage:
#   ./run-search.sh
#   PROTOCOLS="npm cargo" ./run-search.sh
#   KEEP=1 PROTOCOLS=go ./run-search.sh
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NS="${NS:-temp}"
TOOL_IMAGE_PREFIX="${TOOL_IMAGE_PREFIX:-forgejo.develop.10.199.64.20.nip.io/easylab/tool}"
TOOL_TAG="${TOOL_TAG:-latest}"
EASYPX_IMAGE="${EASYPX_IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/easyproxy:v0.5.2}"
CA_SECRET="${CA_SECRET:-artifact-e2e-ca}"
UPSTREAM_DNS="${UPSTREAM_DNS:-172.18.0.10}"
UPSTREAM_PROXY="${UPSTREAM_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
PROTOCOLS=(${PROTOCOLS:-npm cargo rubygems composer hex go pypi nuget helm conda rpm})
KEEP="${KEEP:-0}"

source "${HERE}/rules.sh"

pass=0; fail=0
declare -a RESULTS

# search_cmd <proto>: the shell command run in the tool image. It must exit
# non-zero (or print a sentinel) when the search yields no upstream results.
search_cmd() {
  case "$1" in
    npm)
      cat <<'SH'
cd /tmp && npm search express --no-audit --no-fund --json 2>/dev/null \
  | node -e 'let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{const o=JSON.parse(d);if(!o.length)process.exit(1);console.log("npm search:",o.length,"hits, first",o[0].name)})'
SH
      ;;
    cargo)
      cat <<'SH'
cd /tmp && out="$(cargo search anyhow 2>/dev/null)" && echo "$out" | grep -q anyhow && echo "cargo search: $(echo "$out" | head -1)"
SH
      ;;
    rubygems)
      cat <<'SH'
# The API search and the compact index /names both merge upstream.
api="$(curl -s "https://rubygems.org/api/v1/search.json?query=thor")" \
  && echo "$api" | grep -q '"name":"thor"' \
  && echo "rubygems search: $(echo "$api" | head -c 60)"
names="$(curl -s "https://index.rubygems.org/names")" && echo "$names" | grep -qx thor && echo "rubygems names: thor present"
SH
      ;;
    composer)
      cat <<'SH'
cd /tmp && out="$(composer search monolog 2>/dev/null)" && echo "$out" | grep -qi monolog && echo "composer search: $(echo "$out" | head -1)"
SH
      ;;
    hex)
      cat <<'SH'
# hex.pm's search API (/api/packages?search=) — what `mix hex.package search`
# calls; the adapter merges local packages with the upstream result.
out="$(curl -s "https://hex.pm/api/packages?search=jason")" \
  && echo "$out" | grep -qi '"name":"jason"' \
  && echo "hex search: ok ($(echo "$out" | head -c 40))"
SH
      ;;
    go)
      cat <<'SH'
export HOME=/tmp/sg GOPATH=/tmp/sg/go GOMODCACHE=/tmp/sg/go/pkg/mod GOFLAGS=-mod=mod
rm -rf "$HOME" && mkdir -p "$HOME/w" && cd "$HOME/w"
printf 'module t\n\ngo 1.21\n' > go.mod
printf 'package main\nimport _ "golang.org/x/text/language"\nfunc main(){}\n' > main.go
# Default GOSUMDB: the sumdb lookup must succeed through the mirror.
out="$(timeout 180 go mod tidy 2>&1)" && echo "go sumdb: ok ($(echo "$out" | tail -1))"
SH
      ;;
    pypi)
      cat <<'SH'
out="$(pip index versions six 2>&1)" && echo "$out" | grep -qi six && echo "pip index: $(echo "$out" | head -1)"
SH
      ;;
    nuget)
      cat <<'SH'
cd /tmp && mkdir -p /root/.nuget/NuGet && cat > /root/.nuget/NuGet/NuGet.Config <<'X'
<?xml version="1.0" encoding="utf-8"?>
<configuration><packageSources><clear />
<add key="easylab" value="https://api.nuget.org/v3/index.json" />
</packageSources></configuration>
X
out="$(dotnet package search serilog --source https://api.nuget.org/v3/index.json --take 2 2>&1)" \
  && echo "$out" | grep -qi serilog && echo "nuget search: ok"
SH
      ;;
    helm)
      cat <<'SH'
helm repo add easylab https://charts.helm.sh >/dev/null 2>&1 || true
helm repo update >/dev/null 2>&1
out="$(helm search repo easylab/redis 2>&1)" && echo "$out" | grep -q redis && echo "helm search: ok"
SH
      ;;
    conda)
      cat <<'SH'
conda tos accept --override-channels --channel https://repo.anaconda.com/pkgs/main >/dev/null 2>&1 || true
conda tos accept --override-channels --channel https://repo.anaconda.com/pkgs/r >/dev/null 2>&1 || true
out="$(conda search --override-channels -c defaults zlib 2>&1)" && echo "$out" | grep -q zlib && echo "conda search: ok"
SH
      ;;
    rpm)
      cat <<'SH'
cp /etc/easyproxy/ca.crt /etc/pki/ca-trust/source/anchors/easylab.crt 2>/dev/null || true
update-ca-trust 2>/dev/null || true
# Fedora's default repos use metalink (mirrors.fedoraproject.org, not in the
# spoof policy); pin the baseurl so the search is served by the mirror.
rm -f /etc/yum.repos.d/*.repo
printf '[main]\ninstall_weak_deps=False\nreleasever=44\n' > /etc/dnf/dnf.conf
printf '[fedora]\nname=fedora\nbaseurl=https://dl.fedoraproject.org/pub/fedora/linux/releases/43/Everything/x86_64/os/\nenabled=1\ngpgcheck=0\noptional_metadata_types=primary\n' > /etc/yum.repos.d/fedora.repo
out="$(dnf -q --refresh search jq 2>&1)" && echo "$out" | grep -q jq && echo "dnf search: ok"
SH
      ;;
    *) echo "" ;;
  esac
}

write_script_cm() {
  local p="$1" name="search-$1"
  local tmp
  tmp="$(mktemp -d)"
  search_cmd "$p" >"${tmp}/${p}.sh"
  kubectl create configmap "${name}-script" -n "$NS" \
    --from-file="${p}.sh=${tmp}/${p}.sh" \
    --dry-run=client -o yaml | kubectl apply -n "$NS" -f - >/dev/null
  rm -rf "$tmp"
}

write_pod() {
  local p="$1" name="search-$1"
  parse_row "$p"
  cat <<YAML | kubectl apply -n "$NS" -f - >/dev/null
apiVersion: v1
kind: Pod
metadata: { name: ${name}, labels: { app: search } }
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

rewrite_count() { kubectl logs -n "$NS" "$1" -c easyproxy 2>/dev/null | grep -c '"action":"rewrite"'; }

run_one() {
  local p="$1" name="search-$1" out rc rewrites
  if [ -z "$(search_cmd "$p")" ]; then
    echo "SKIP $p  (no search command)"; return
  fi
  write_rules_cm search "$p"
  write_script_cm "$p"
  kubectl delete pod -n "$NS" "$name" --ignore-not-found --force --grace-period=0 >/dev/null 2>&1
  write_pod "$p"
  if ! kubectl wait -n "$NS" --for=condition=Ready "pod/${name}" --timeout=300s >/dev/null 2>&1; then
    echo "FAIL $p  (pod not ready)"; fail=$((fail+1)); RESULTS+=("FAIL $p pod-not-ready"); return
  fi
  out="$(kubectl exec -n "$NS" "$name" -c tool -- timeout 400 sh "/scripts/${p}.sh" 2>&1)"; rc=$?
  rewrites="$(rewrite_count "$name")"
  if [ "$rc" -eq 0 ] && [ "${rewrites:-0}" -gt 0 ]; then
    echo "PASS $p  ($rewrites conns)  $(echo "$out" | tail -1)"
    pass=$((pass+1)); RESULTS+=("PASS $p ($rewrites conns)")
  elif [ "$rc" -eq 0 ]; then
    echo "FAIL $p  (ok but bypassed)"; fail=$((fail+1)); RESULTS+=("FAIL $p bypassed")
    echo "$out" | tail -6 | sed 's/^/      | /'
  else
    echo "FAIL $p  (rc=$rc)"; fail=$((fail+1)); RESULTS+=("FAIL $p rc=$rc")
    echo "$out" | tail -10 | sed 's/^/      | /'
  fi
  [ "$KEEP" = "1" ] || kubectl delete pod -n "$NS" "$name" --ignore-not-found >/dev/null 2>&1
}

for p in "${PROTOCOLS[@]}"; do run_one "$p"; done

echo "====================================="
printf '%s\n' "${RESULTS[@]}"
echo "SEARCH MATRIX: $pass pass, $fail fail"
[ "$fail" -eq 0 ]
