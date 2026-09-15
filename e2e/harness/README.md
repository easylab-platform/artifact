# artifact pull-through e2e harness

Verifies each protocol adapter end-to-end through the spoof egress policy:
DNS-spoof steers the package-manager hostname at the easyproxy sidecar, which
MITMs TLS and proxies the (path-prefixed) request to the per-protocol artifact
Service. Success = the client's package manager installs a package AND the
sidecar logged at least one `rewrite` connection (proving the request went
through the proxy rather than direct).

## Layout
- `deploy-protocol.sh` — one Deployment+Service per protocol (`artifact-<p>`);
  each mounts exactly one protocol + serves it on port 80.
- `gen-ca.sh` — generate the fixed dev-only egress MITM CA and create the
  `artifact-e2e-ca` secret (re-run to rotate).
- `run.sh` — matrix driver: per protocol it creates a tool+easyproxy pod
  (spoof DNS via `dnsPolicy: None`), runs the client command, and asserts the
  sidecar saw a rewrite.

## Usage
```sh
./gen-ca.sh                                   # once
NS=temp ./deploy-protocol.sh                  # all protocols
PROTOCOLS="npm pypi go cargo maven" ./run.sh  # subset matrix
KEEP=1 PROTOCOLS="cargo" ./run.sh             # keep the pod for debugging
```

## Prereqs
- namespace + images: build/push `artifact` and `easyproxy` first
  (`../build-image.sh`, `../../easyproxy/build-image.sh`);
  `tool` images are pulled through easylab's own OCI mirror
  (`easylab.temp.svc.cluster.local:80/docker.io/...`).
- easyproxy >= v0.5.0 (keep-alive request rewriting: every request on a
  connection is re-originated with its path mapped, not just the first).
- Tools whose TLS trust store is not file-env driven (Java/Maven) need the CA
  imported explicitly — `run.sh` does this via keytool + JAVA_TOOL_OPTIONS.
