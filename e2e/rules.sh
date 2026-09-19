#!/usr/bin/env bash
# Shared easysidecar rule construction for the e2e harnesses (pull matrix +
# lifecycle). Sourced, not executed. Provides proto_row / parse_row /
# write_rules_cm.
#
# Per-protocol columns (pipe separated, values must not contain "|"):
#   match json | strip_prefix | add_prefix
# The tool images are ${TOOL_IMAGE_PREFIX}-<proto>:${TOOL_TAG}.

RULES_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

proto_row() {
  case "$1" in
    npm)         echo '["registry.npmjs.org", "*.npmjs.org"]||/artifacts/npm' ;;
    pypi)        echo '["pypi.org", "files.pythonhosted.org"]||/artifacts/pypi' ;;
    go)          echo '["proxy.golang.org", "sum.golang.org"]||/artifacts/go' ;;
    cargo)       echo '["index.crates.io", "crates.io", "static.crates.io"]||/artifacts/cargo' ;;
    maven)       echo '["repo.maven.apache.org"]|/maven2|/artifacts/maven' ;;
    nuget)       echo '["api.nuget.org", "azuresearch-usnc.nuget.org"]||/artifacts/nuget' ;;
    rubygems)    echo '["rubygems.org", "index.rubygems.org"]||/artifacts/rubygems' ;;
    composer)    echo '["repo.packagist.org"]||/artifacts/composer' ;;
    hex)         echo '["repo.hex.pm", "hex.pm", "api.hex.pm"]||/artifacts/hex' ;;
    pub)         echo '["pub.dev"]||/artifacts/pub' ;;
    helm)        echo '["charts.helm.sh"]|/stable|/artifacts/helm' ;;
    conan)       echo '["center.conan.io", "center2.conan.io"]||/artifacts/conan' ;;
    swift)       echo '["api.spm.swift.org"]||/artifacts/swift' ;;
    conda)       echo '["repo.anaconda.com", "conda.anaconda.org"]||/artifacts/conda' ;;
    nix)         echo '["cache.nixos.org"]||/artifacts/nix' ;;
    huggingface) echo '["huggingface.co", "*.huggingface.co", "cdn-lfs.huggingface.co"]||/artifacts/huggingface' ;;
    protobuf)    echo '["buf.build"]||/artifacts/protobuf' ;;
    debian)      echo '["deb.debian.org", "security.debian.org", "archive.ubuntu.com", "security.ubuntu.com"]||/artifacts/debian' ;;
    apk)         echo '["dl-cdn.alpinelinux.org"]||/artifacts/apk' ;;
    rpm)         echo '["dl.fedoraproject.org"]||/artifacts/rpm' ;;
    oci)         echo '["registry-1.docker.io", "docker.io", "index.docker.io", "ghcr.io", "quay.io"]||' ;;
    hackage)     echo '["hackage.haskell.org"]||/artifacts/hackage' ;;
    cran)        echo '["cran.r-project.org"]||/artifacts/cran' ;;
    cpan)        echo '["cpan.metacpan.org", "www.cpan.org", "cpan.org"]||/artifacts/cpan' ;;
    luarocks)    echo '["luarocks.org"]||/artifacts/luarocks' ;;
    juliapkg)    echo '["pkg.julialang.org", "*.pkg.julialang.org"]||/artifacts/juliapkg' ;;
    git)         echo '["github.com"]||/artifacts/git' ;;
    gitlfs)      echo '["github.com"]||/artifacts/git' ;;
    netcache)    echo '["raw.githubusercontent.com"]||/artifacts/netcache' ;;
    ivy)         echo '["repo.scala-sbt.org"]||/artifacts/ivy' ;;
    google)      echo '["dl.google.com"]|/dl/android/maven2|/artifacts/maven' ;;
    gradle)      echo '["plugins.gradle.org"]|/m2|/artifacts/maven' ;;
    clojars)     echo '["repo.clojars.org"]||/artifacts/maven' ;;
    spring)      echo '["repo.spring.io"]|/milestone|/artifacts/maven' ;;
    jitpack)     echo '["jitpack.io"]||/artifacts/maven' ;;
    jsr)         echo '["jsr.io"]||/artifacts/jsr' ;;
    jsrnpm)      echo '["npm.jsr.io"]||/artifacts/npm' ;;
    opam)        echo '["opam.ocaml.org"]||/artifacts/opam' ;;
    stackage)    echo '["stackage.org"]||/artifacts/stackage' ;;
    pecl)        echo '["pecl.php.net"]||/artifacts/pecl' ;;
    bazel)       echo '["bcr.bazel.build"]||/artifacts/bazel' ;;
    jenkins)     echo '["updates.jenkins.io"]||/artifacts/jenkins' ;;
    *)           echo "" ;;
  esac
}

parse_row() { # sets R_IMG R_MATCH R_STRIP R_ADD
  IFS='|' read -r R_MATCH R_STRIP R_ADD <<<"$(proto_row "$1")"
  # rpm/apk/nix use native Fedora/Alpine/Nix bases, independent of the debian
  # variant, so they always use the default tag.
  case "$1" in
    rpm|apk|nix) R_IMG="${TOOL_IMAGE_PREFIX}-$1:latest" ;;
    # New mirrors reuse an existing client image: the maven-layout mirrors use
    # the maven tool, the plain-HTTP trees the haskell/curl tool.
    google|gradle|clojars|spring|jitpack) R_IMG="${TOOL_IMAGE_PREFIX}-maven:${TOOL_TAG}" ;;
    jsrnpm)  R_IMG="${TOOL_IMAGE_PREFIX}-npm:${TOOL_TAG}" ;;
    jsr)     R_IMG="${TOOL_IMAGE_PREFIX}-haskell:${TOOL_TAG}" ;;
    gitlfs)  R_IMG="${TOOL_IMAGE_PREFIX}-gitlfs:${TOOL_TAG}" ;;
    opam|stackage|pecl|bazel|jenkins) R_IMG="${TOOL_IMAGE_PREFIX}-haskell:${TOOL_TAG}" ;;
    netcache) R_IMG="${TOOL_IMAGE_PREFIX}-git:${TOOL_TAG}" ;;
    *)           R_IMG="${TOOL_IMAGE_PREFIX}-$1:${TOOL_TAG}" ;;
  esac
}

# write_rules_cm <name-prefix> <proto> writes ConfigMap <name-prefix>-<proto>-rules
# into NS, mapping the protocol's upstream hostnames onto artifact-<proto>.
# UNIFIED_SVC, when set, makes every rule target that one Service instead (the
# unified-mount topology: only the add_prefix differs between rules).
write_rules_cm() {
  local p="$2" prefix="$1"
  local name="${prefix}-$2"
  parse_row "$p"
  local target="artifact-${p}.${NS}.svc.cluster.local:80"
  if [ -n "${UNIFIED_SVC:-}" ]; then
    target="${UNIFIED_SVC}.${NS}.svc.cluster.local:80"
  fi
  {
    echo "apiVersion: v1"
    echo "kind: ConfigMap"
    echo "metadata:"
    echo "  name: ${name}-rules"
    echo "data:"
    echo "  rules.yaml: |"
    echo "    rules:"
    # Extras first (first match wins), then the main rewrite rule. A
    # bare-suffix main rule (e.g. "hex.pm") also matches subdomains
    # (builds.hex.pm), so direct carve-outs must precede it.
    if [ -f "${RULES_DIR}/scripts/${p}.rules.yaml" ]; then
      sed 's/^/      /' "${RULES_DIR}/scripts/${p}.rules.yaml"
    fi
    echo "      - match: ${R_MATCH}"
    echo "        action: rewrite"
    echo "        target: \"${target}\""
    [ -n "$R_STRIP" ] && echo "        strip_prefix: \"${R_STRIP}\""
    [ -n "$R_ADD" ] && echo "        add_prefix: \"${R_ADD}\""
    echo "    default: direct"
    echo "    mitm_default: false"
  } | kubectl apply -n "$NS" -f - >/dev/null
}
