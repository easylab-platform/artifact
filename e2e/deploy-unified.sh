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
PROTOCOLS="${PROTOCOLS:-npm pypi go cargo maven nuget rubygems composer hex pub helm conan swift conda nix huggingface protobuf debian apk rpm oci git ivy hackage cran cpan luarocks juliapkg google gradle clojars spring jitpack jsr jsrnpm opam stackage pecl bazel jenkins gitlfs netcache system}"

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
    git)         echo "https://github.com" ;;
    hackage)     echo "https://hackage.haskell.org" ;;
    cran)        echo "https://cran.r-project.org" ;;
    cpan)        echo "https://cpan.metacpan.org" ;;
    luarocks)    echo "https://luarocks.org" ;;
    juliapkg)    echo "https://pkg.julialang.org" ;;
    ivy)         echo "https://repo.scala-sbt.org" ;;
    google)      echo "https://dl.google.com/dl/android/maven2" ;;
    gradle)      echo "https://plugins.gradle.org/m2" ;;
    clojars)     echo "https://repo.clojars.org" ;;
    spring)      echo "https://repo.spring.io/milestone" ;;
    jitpack)     echo "https://jitpack.io" ;;
    jsr)         echo "https://jsr.io" ;;
    jsrnpm)      echo "https://npm.jsr.io" ;;
    opam)        echo "https://opam.ocaml.org" ;;
    stackage)    echo "https://stackage.org" ;;
    pecl)        echo "https://pecl.php.net" ;;
    bazel)       echo "https://bcr.bazel.build" ;;
    jenkins)     echo "https://updates.jenkins.io" ;;
    gitlfs)      echo "https://github.com" ;;
    *)           echo "" ;;
  esac
}

# proto_mount maps an e2e row onto the artifact adapter that serves it
# (mirrors ride an existing adapter), so --protocols lists distinct adapters.
proto_mount() {
  case "$1" in
    google|gradle|clojars|spring|jitpack) echo maven ;;
    jsrnpm) echo npm ;;
    gitlfs) echo git ;;
    *) echo "$1" ;;
  esac
}

map=""
list=""
seen_mount=""
seen_map=""
for p in $PROTOCOLS; do
  mount="$(proto_mount "$p")"
  # --protocols: distinct adapters only.
  case ",${seen_mount}," in
    *",${mount},"*) : ;;
    *) seen_mount="${seen_mount:+${seen_mount},}${mount}"
       list="${list:+${list},}${mount}" ;;
  esac
  o="$(proto_origin "$p")"
  [ -n "$o" ] || continue
  # self-base-map is keyed by adapter; the protocol's own origin wins over a
  # later mirror entry (first occurrence), so maven keeps its Central shape.
  case ",${seen_map}," in
    *",${mount},"*) continue ;;
  esac
  seen_map="${seen_map:+${seen_map},}${mount}"
  map="${map:+${map},}${mount}=${o}"
done

extra_args=""
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
          httpGet: { path: /readyz, port: http }
          initialDelaySeconds: 3
          periodSeconds: 5
        livenessProbe:
          httpGet: { path: /healthz, port: http }
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
