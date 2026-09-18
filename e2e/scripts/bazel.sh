#!/bin/sh
# Bazel Central Registry (bcr.bazel.build): module metadata + source archive ref.
set -e
CA=/etc/easysidecar/ca.crt
W="wget --ca-certificate=$CA -qO"
$W /tmp/bcr.json "https://bcr.bazel.build/modules/rules_go/metadata.json"
grep -q 'rules_go' /tmp/bcr.json || { echo "bazel: bad metadata"; exit 1; }
$W /tmp/bcr-src.json "https://bcr.bazel.build/modules/rules_go/0.50.1/source.json"
grep -q 'url' /tmp/bcr-src.json || { echo "bazel: bad source.json"; exit 1; }
echo "bazel: metadata + source.json ok"
