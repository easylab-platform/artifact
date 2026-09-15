#!/bin/sh
set -e
# Erlang's hex client uses its own CA bundle (not SSL_CERT_FILE), so install
# the egress CA into the system trust store and point hex at it.
cp /etc/easyproxy/ca.crt /usr/local/share/ca-certificates/easylab.crt 2>/dev/null || true
update-ca-certificates >/dev/null 2>&1 || true
cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
export HEX_CACERTS_PATH=/etc/ssl/certs/ca-certificates.crt
export SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt
mix local.hex --force >/dev/null
mix local.rebar --force >/dev/null
mix hex.package fetch jason 1.4.4 --unpack -o /tmp/jason
