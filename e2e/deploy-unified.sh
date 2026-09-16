#!/usr/bin/env bash
# Deploy ONE artifact Deployment+Service mounting every protocol, with a
# per-protocol self-base map (-self-base-map) so each adapter emits its own
# upstream-shaped URLs. This is the "unified mount" topology: all sidecar
# rules point at the same Service and differ only by add_prefix.
set -euo pipefail
NS="${NS:-temp}"
IMAGE="${IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/artifact:latest}"
UPSTREAM_PROXY="${UPSTREAM_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
NAME="${NAME:-artifact-unified}"
# Protocol list mirrors run.sh's default matrix.
PROTOCOLS="${PROTOCOLS:-npm pypi go cargo maven nuget rubygems composer hex pub helm conan swift conda nix huggingface protobuf debian apk rpm oci}"

# Upstream origins (must match rules.sh proto_row hostnames).
proto_origin() {
  case "$1" in
    npm)         echo "https://registry.npmjs.org" ;;
    pypi)        echo "https://pypi.org" ;;
    go)          echo "https://proxy.golang.org" ;;
    cargo)       echo "https://index.crates.io" ;;
    maven)       echo "https://repo.maven.apache.org/maven2" ;;
    nuget)       echo "https://api.nuget.org" ;;
    rubygems)    echo "https://rubygems.org" ;;
    composer)    echo "https://repo.packagist.org" ;;
    hex)         echo "https://repo.hex.pm" ;;
    pub)         echo "https://pub.dev" ;;
    helm)        echo "https://charts.helm.sh" ;;
    conan)       echo "https://center2.conan.io" ;;
    swift)       echo "https://api.spm.swift.org" ;;
    conda)       echo "https://repo.anaconda.com" ;;
    nix)         echo "https://cache.nixos.org" ;;
    huggingface) echo "https://huggingface.co" ;;
    protobuf)    echo "https://buf.build" ;;
    debian)      echo "http://deb.debian.org" ;;
    apk)         echo "http://dl-cdn.alpinelinux.org" ;;
    rpm)         echo "https://dl.fedoraproject.org" ;;
    oci)         echo "https://registry-1.docker.io" ;;
    *)           echo "" ;;
  esac
}

map=""
list=""
for p in $PROTOCOLS; do
  o="$(proto_origin "$p")"
  [ -n "$o" ] || continue
  [ -n "$map" ] && map="${map},"
  map="${map}${p}=${o}"
  [ -n "$list" ] && list="${list},"
  list="${list}${p}"
done

# The Fedora client baseurl is .../pub/fedora/...; the rpm adapter treats the
# first segment ("pub") as the repo key, so its base is .../pub.
extra_args=""
case ",${list}," in
  *,rpm,*) extra_args=', "--upstreams=rpm=https://dl.fedoraproject.org/pub"' ;;
esac

cat <<YAML | kubectl apply -n "${NS}" -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${NAME}
  labels: { app: artifact-unified }
spec:
  replicas: 1
  strategy: { type: Recreate }
  selector: { matchLabels: { app: ${NAME} } }
  template:
    metadata: { labels: { app: ${NAME} } }
    spec:
      containers:
      - name: artifact
        image: ${IMAGE}
        args: ["--listen=:8080", "--data=/data", "--protocols=${list}", "--self-base-map=${map}"${extra_args}]
        ports: [{ name: http, containerPort: 8080 }]
        resources:
          requests: { cpu: 200m, memory: 256Mi }
          limits:   { cpu: "2", memory: 2Gi }
        env:
        - name: HTTP_PROXY
          value: "${UPSTREAM_PROXY}"
        - name: HTTPS_PROXY
          value: "${UPSTREAM_PROXY}"
        - name: NO_PROXY
          value: "localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io"
        - name: ARTIFACT_MAX_BODY
          value: "34359738368"
        readinessProbe:
          tcpSocket: { port: http }
          initialDelaySeconds: 3
          periodSeconds: 5
        livenessProbe:
          tcpSocket: { port: http }
          initialDelaySeconds: 15
          periodSeconds: 20
        volumeMounts: [{ name: data, mountPath: /data }]
      volumes: [{ name: data, emptyDir: {} }]
---
apiVersion: v1
kind: Service
metadata:
  name: ${NAME}
  labels: { app: artifact-unified }
spec:
  selector: { app: ${NAME} }
  ports: [{ name: http, port: 80, targetPort: http }]
YAML
echo "deployed unified: ${NAME} (${list})"
