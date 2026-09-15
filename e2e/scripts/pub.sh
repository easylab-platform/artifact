#!/bin/sh
set -e
# Use the official dart image; its pub honors the system trust store, so add
# the egress CA and let the spoofed proxy intercept pub.dev.
cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
dart pub cache add http --version 1.2.2
