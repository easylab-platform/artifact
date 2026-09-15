#!/bin/sh
set -e
# Native Alpine base: apk works as-is. Trust the egress CA and point the repo
# at the real CDN (intercepted by the spoofed proxy -> artifact-apk).
cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt
printf 'https://dl-cdn.alpinelinux.org/alpine/v3.24/main\nhttps://dl-cdn.alpinelinux.org/alpine/v3.24/community\n' > /etc/apk/repositories
apk update
# --allow-untrusted: the artifact mirror serves the upstream index byte-for-byte
# but this test trusts the egress CA rather than the Alpine signing keys.
apk add --no-cache --allow-untrusted jq
