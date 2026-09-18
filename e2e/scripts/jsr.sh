#!/bin/sh
# JSR native registry API (jsr.io): package metadata + a module file. These are
# plain GETs on a path tree (meta.json, <ver>_meta.json, <ver>/<path>).
set -e
CA=/etc/easysidecar/ca.crt
W="wget --ca-certificate=$CA -qO"
$W /tmp/meta.json "https://jsr.io/@luca/flag/meta.json"
grep -q '"scope": *"luca"' /tmp/meta.json || { echo "jsr: bad meta"; exit 1; }
$W /tmp/vmeta.json "https://jsr.io/@luca/flag/1.0.1_meta.json"
grep -q 'manifest' /tmp/vmeta.json || { echo "jsr: bad version meta"; exit 1; }
$W /tmp/main.ts "https://jsr.io/@luca/flag/1.0.1/main.ts"
grep -qi 'export' /tmp/main.ts || { echo "jsr: bad module"; exit 1; }
echo "jsr: meta + version meta + module ok"
