# artifact pull-through e2e harness

Verifies each protocol adapter end-to-end through the spoof egress policy:
DNS-spoof steers the package-manager hostname at the easyproxy sidecar, which
MITMs TLS and proxies the (path-prefixed/stripped) request to the per-protocol
artifact Service. Success = the client's package manager installs/downloads a
package through the proxy AND the sidecar logged >=1 `rewrite` connection
(proving the request was not bypassed).

Current status: **20/20 protocols PASS** (see the "Deviations" section for
`nuget`, which is excluded by default for environment reasons).

## Layout
- `deploy-protocol.sh` — one Deployment+Service per protocol (`artifact-<p>`);
  each mounts exactly one protocol + serves it on port 80.
- `gen-ca.sh` — generate the fixed dev-only egress MITM CA and create the
  `artifact-e2e-ca` secret (re-run to rotate).
- `run.sh` — matrix driver: per protocol it creates a tool+easyproxy pod
  (spoof DNS via `dnsPolicy: None`), runs `scripts/<p>.sh`, and asserts the
  sidecar saw a rewrite.
- `scripts/<p>.sh` — the client command per protocol, mounted at `/scripts`.
- `scripts/<p>.rules.yaml` — optional extra rewrite rules for that protocol's
  pod (e.g. `conan.rules.yaml` routes its PyPI bootstrap through artifact-pypi).

## Usage
```sh
./gen-ca.sh                                   # once
NS=temp ./deploy-protocol.sh                  # all protocols
PROTOCOLS="npm pypi go cargo maven" ./run.sh  # subset matrix
KEEP=1 PROTOCOLS=cargo ./run.sh               # keep the pod for debugging
```

## Prereqs
- namespace + images: build/push `artifact` and `easyproxy` first
  (`../build-image.sh`, `../../easyproxy/build-image.sh`); tool images are
  pulled through easylab's own OCI mirror
  (`easylab.${NS}.svc.cluster.local:80/docker.io/...`).
- easyproxy >= v0.5.0 (keep-alive request rewriting: every request on a
  connection is re-originated with its path mapped, not just the first).
- artifact >= v0.1.14 (oci blob redirects, per-protocol routing fixes).

## Notes / gotchas
- Tools whose TLS trust store is not file-env driven need the CA imported
  explicitly: `maven` (keytool + JAVA_TOOL_OPTIONS), `hex` (HEX_CACERTS_PATH),
  `pub` (dart `--root-certs-file`), `rpm` (update-ca-trust), `oci`/`debian`
  (append to the system bundle).
- `oci` resolves a manifest list and GETs a layer, exercising the 307→CDN
  redirect the adapter now follows server-side.
- Clients that emit several requests per keep-alive connection (cargo, maven,
  npm) are the reason the proxy rewrites every request, not just the first.

## Deviations
- **nuget** (`mcr.microsoft.com/dotnet/sdk`) is excluded from the default
  list: its layers come off a Southeast-Asia Azure CDN that this cluster's
  egress reaches at ~80KB/s, so the ~18MB layer pull times out. It uses the
  same HTTP pull-through path as every other protocol; run it explicitly where
  the CDN is fast.
- **swift**: SwiftPM has NO public registry upstream (`api.spm.swift.org` and
  `packages.swift.org` are NXDOMAIN as of 2026-09); its normal resolution path
  is a git clone from GitHub. The script therefore exercises the adapter's
  SCM-to-registry bridge (register a git URL, enumerate tags, serve a source
  archive) through the spoofed proxy.
- **protobuf**: the Buf Schema Registry is a Connect/gRPC service; the `buf`
  CLI talks gRPC directly, so the script drives the adapter's HTTP `/v1` face
  through the spoofed proxy and asserts the response is fetched and served.
