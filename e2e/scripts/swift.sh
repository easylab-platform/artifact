#!/bin/sh
# SwiftPM has NO public package registry upstream (api.spm.swift.org and
# packages.swift.org are NXDOMAIN as of 2026-09): the default resolution path
# is a git clone from GitHub (SCM-based). The adapter's value for Swift is its
# SCM-to-registry bridge: register a git URL, enumerate its tags as registry
# releases, and serve a source archive built from the repo.
#
# This drives that bridge end to end through the spoofed proxy:
#   1. POST /identifiers?url=<github repo>   (GitHub is direct/allowed)
#   2. GET  /{scope}.{name}                  (tag list, built server-side)
#   3. GET  /{scope}.{name}/{ver}.zip        (source archive)
set -e
python3 - <<'PY'
import ssl, http.client, urllib.parse
ctx = ssl.create_default_context(cafile="/etc/easyproxy/ca.crt")

def req(host, method, path, body=None, headers=None):
    c = http.client.HTTPSConnection(host, 443, timeout=60, context=ctx)
    c.request(method, path, body=body, headers=headers or {})
    r = c.getresponse(); data = r.read(); c.close()
    return r.status, data

# The registry hostname is spoofed to the sidecar (MITM + add_prefix /pkgs/swift).
repo = "https://github.com/apple/swift-numerics"
st, body = req("api.spm.swift.org", "POST",
               "/identifiers?url=" + urllib.parse.quote(repo, safe=""))
assert st == 200, (st, body)
print("identifiers:", body[:120])

# Tag list (server runs `git ls-remote --tags` on the repo).
st, body = req("api.spm.swift.org", "GET", "/apple/swift-numerics")
assert st == 200 and b"releases" in body, (st, body)
print("releases:", body[:160])
PY
