#!/bin/sh
set -e
# Erlang's TLS uses its own cacerts file (not SSL_CERT_FILE).
cat /etc/ssl/certs/ca-certificates.crt > /tmp/cacerts.pem 2>/dev/null || true
cat /etc/easyproxy/ca.crt >> /tmp/cacerts.pem
export HEX_CACERTS_PATH=/tmp/cacerts.pem
mix local.rebar --force >/dev/null 2>&1 || true
mix archive.install /opt/hex-archive/hex.ez --force >/dev/null
mix hex.package fetch jason 1.4.4 --unpack -o /tmp/jason
