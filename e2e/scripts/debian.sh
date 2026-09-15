#!/bin/sh
set -e
# Route apt at the sidecar's HTTP face explicitly. On bookworm the spoofed DNS
# alone steers apt; on trixie apt 3.0's http/mirror method resolves around the
# spoofed resolver, so pinning the proxy (the sidecar) makes the path
# deterministic for both — every request still goes sidecar -> artifact-debian.
APT_PROXY="Acquire::http::Proxy=http://127.0.0.1:80"
apt-get update -o "$APT_PROXY" -o Acquire::Retries=0
apt-get install -y --no-install-recommends -o "$APT_PROXY" ca-certificates jq
