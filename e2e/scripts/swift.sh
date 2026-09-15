#!/bin/sh
set -e
# SwiftPM has NO public registry upstream (api.spm.swift.org is NXDOMAIN), so
# exercise the adapter's SCM-to-registry bridge through the spoofed proxy:
# register a git URL, enumerate its tags, and (server-side) build a source
# archive.
python3 - <<'PY'
import ssl, http.client, urllib.parse
ctx = ssl.create_default_context(cafile="/etc/easyproxy/ca.crt")
def req(method, path):
    c = http.client.HTTPSConnection("api.spm.swift.org", 443, timeout=120, context=ctx)
    c.request(method, path); r = c.getresponse(); body = r.read(); c.close()
    return r.status, body
repo = "https://github.com/apple/swift-numerics"
st, body = req("POST", "/identifiers?url=" + urllib.parse.quote(repo, safe=""))
assert st == 200, (st, body)
print("identifiers:", body[:100])
st, body = req("GET", "/apple/swift-numerics")
assert st == 200 and b"releases" in body, (st, body)
print("releases:", body[:140])
PY
