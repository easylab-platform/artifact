#!/bin/sh
set -e
# apk-tools-static manages an Alpine root under /apkroot; the package/repo URL
# is the real CDN host, intercepted by the spoofed proxy.
ROOT=/apkroot
mkdir -p "$ROOT/etc/ssl/certs"
cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt
apk --root "$ROOT" --initdb --arch x86_64 --allow-untrusted \
  --repository https://dl-cdn.alpinelinux.org/alpine/v3.23/main add ca-certificates
cp /etc/ssl/certs/ca-certificates.crt "$ROOT/etc/ssl/certs/ca-certificates.crt"
apk --root "$ROOT" --allow-untrusted \
  --repository https://dl-cdn.alpinelinux.org/alpine/v3.23/main add jq
