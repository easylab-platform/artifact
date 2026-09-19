#!/usr/bin/env bash
# Deploy one artifact Deployment+Service per protocol into the e2e namespace.
# Each deployment mounts exactly one protocol (isolation: its upstream fetches
# never traverse another protocol's sidecar). The Service name is artifact-<p>.
set -euo pipefail
NS="${NS:-temp}"
IMAGE="${IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/artifact:v0.20.0}"
UPSTREAM_PROXY="${UPSTREAM_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
PROTOCOLS=(${PROTOCOLS:-npm pypi go cargo maven nuget rubygems composer hex pub helm conan swift conda nix huggingface protobuf debian apk rpm oci git ivy hackage cran cpan luarocks juliapkg system google gradle clojars spring jitpack jsr jsrnpm opam stackage pecl bazel jenkins gitlfs netcache})

# proto_mount maps an e2e protocol row to the artifact protocol it mounts (the
# maven-layout mirrors reuse the maven adapter; jsrnpm reuses npm).
proto_mount() {
  case "$1" in
    google|gradle|clojars|spring|jitpack) echo maven ;;
    jsrnpm) echo npm ;;
    gitlfs) echo git ;;
    *) echo "$1" ;;
  esac
}

# ORIGIN=1 mounts each adapter in "upstream origin" mode (--self-base-raw):
# self URLs take the upstream shape (registry.npmjs.org/left-pad/-/x.tgz,
# pypi.org/simple/...) so a spoofing sidecar maps them back with add_prefix.
# The origins must match the hostnames in run.sh's proto_row rules.
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
    gitlfs)      echo "https://github.com" ;;
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
    *)           echo "" ;;
  esac
}

ORIGIN="${ORIGIN:-1}"

for p in "${PROTOCOLS[@]}"; do
  name="artifact-${p}"
  mount="$(proto_mount "$p")"
  extra=""
  extra_args=""
  self_base="http://${name}.${NS}.svc.cluster.local"
  if [ "$ORIGIN" = "1" ] && [ -n "$(proto_origin "$p")" ]; then
    self_base="$(proto_origin "$p")"
    extra_args="${extra_args}, \"--self-base-raw\""
  fi
  if [ "$p" = "oci" ]; then
    extra=$'        - name: ARTIFACT_MAX_BODY\n          value: "34359738368"\n'
  fi
  cat <<YAML | kubectl apply -n "${NS}" -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  labels: { app: artifact-e2e, protocol: "${p}" }
spec:
  replicas: 1
  strategy: { type: Recreate }
  selector: { matchLabels: { app: ${name} } }
  template:
    metadata: { labels: { app: ${name} } }
    spec:
      containers:
      - name: artifact
        image: ${IMAGE}
        args: ["--listen=:8080", "--data=/data", "--protocols=${mount}", "--self-base=${self_base}"${extra_args}]
        ports: [{ name: http, containerPort: 8080 }]
        resources:
          requests: { cpu: 50m, memory: 64Mi }
          limits:   { cpu: 500m, memory: 512Mi }
        env:
        - name: HTTP_PROXY
          value: "${UPSTREAM_PROXY}"
        - name: HTTPS_PROXY
          value: "${UPSTREAM_PROXY}"
        - name: NO_PROXY
          value: "localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io"
${extra}        readinessProbe:
          tcpSocket: { port: http }
          initialDelaySeconds: 2
          periodSeconds: 5
        livenessProbe:
          tcpSocket: { port: http }
          initialDelaySeconds: 10
          periodSeconds: 15
        volumeMounts: [{ name: data, mountPath: /data }]
      volumes: [{ name: data, emptyDir: {} }]
---
apiVersion: v1
kind: Service
metadata:
  name: ${name}
  labels: { app: artifact-e2e, protocol: "${p}" }
spec:
  selector: { app: ${name} }
  ports: [{ name: http, port: 80, targetPort: http }]
YAML
done
echo "deployed: ${PROTOCOLS[*]}"
