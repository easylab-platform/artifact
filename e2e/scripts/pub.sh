#!/bin/sh
set -e
# Dart's pub verifies TLS against its own root set and ignores SSL_CERT_FILE;
# it honors the VM's --root-certs-file flag, so pass the egress CA bundle.
BUNDLE=/tmp/easylab-ca.pem
cat /etc/ssl/certs/ca-certificates.crt > "$BUNDLE" 2>/dev/null || true
cat /etc/easyproxy/ca.crt >> "$BUNDLE"
dart --root-certs-file="$BUNDLE" pub cache add http --version 1.2.2
