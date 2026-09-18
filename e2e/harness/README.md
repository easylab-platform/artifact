# artifact pull-through e2e harness

Verifies each protocol adapter end-to-end through the spoof egress policy:
DNS-spoof steers the package-manager hostname at the easysidecar sidecar, which
MITMs TLS and proxies the (path-prefixed/stripped) request to the per-protocol
artifact Service. Success = the client's package manager installs/downloads a
package through the proxy AND the sidecar logged >=1 `rewrite` connection
(proving the request was not bypassed).

Current status: **28/28 protocols PASS** (see the "Deviations" section for
hosts that are environment-sensitive).

## Layout
- `deploy-protocol.sh` — one Deployment+Service per protocol (`artifact-<p>`);
  each mounts exactly one protocol + serves it on port 80.
- `gen-ca.sh` — generate the fixed dev-only egress MITM CA and create the
  `artifact-e2e-ca` secret (re-run to rotate).
- `run.sh` — matrix driver: per protocol it creates a tool+easysidecar pod
  (spoof DNS via `dnsPolicy: None`), runs `scripts/<p>.sh`, and asserts the
  sidecar saw a rewrite. Protocols run concurrently (`JOBS`, default 6).
- `parallel.sh` — shared bounded worker pool: `run_pool <fn> <proto...>` runs
  `fn` for each protocol with at most `JOBS` in flight, captures each worker's
  log to its own file, then prints them in protocol order and aggregates the
  PASS/FAIL/SKIP result files. `JOBS=1` reproduces serial execution.
- `scripts/<p>.sh` — the client command per protocol, mounted at `/scripts`.
- `scripts/<p>.rules.yaml` — optional extra rewrite rules for that protocol's
  pod (e.g. `conan.rules.yaml` routes its PyPI bootstrap through artifact-pypi).

## Usage
```sh
./gen-ca.sh                                   # once
NS=temp ./deploy-protocol.sh                  # all protocols
PROTOCOLS="npm pypi go cargo maven" ./run.sh  # subset matrix
JOBS=12 ./run.sh                              # more parallelism (default 6)
KEEP=1 PROTOCOLS=cargo ./run.sh               # keep the pod for debugging
```

## Prereqs
- namespace + images: build/push `artifact` and `easysidecar` first
  (`../build-image.sh`, `../../easysidecar/build-image.sh`); tool images are
  pulled through easylab's own OCI mirror
  (`easylab.${NS}.svc.cluster.local:80/docker.io/...`).
- easysidecar >= v0.5.0 (keep-alive request rewriting: every request on a
  connection is re-originated with its path mapped, not just the first).
- artifact >= v0.1.14 (oci blob redirects, per-protocol routing fixes).

## Unified mount
`deploy-unified.sh` deploys ONE artifact Deployment+Service mounting every
protocol, with a per-protocol self-base map (`--self-base-map npm=https://
registry.npmjs.org,pypi=https://pypi.org,...`). Each adapter then emits its own
upstream-shaped self URLs, so all sidecar rules point at the same Service and
differ only by `add_prefix`. `UNIFIED_SVC=artifact-unified ./run.sh` and
`UNIFIED_SVC=artifact-unified ../lifecycle/run.sh` prove the topology is
transparent: 28/28 pull and 18/18 lifecycle pass against one instance.

## Search/aux matrix
`run-search.sh` drives each client's SEARCH (or other auxiliary) endpoint and
asserts real results, with `GOSUMDB` at its default (the go build verifies
against `sum.golang.org`, which the adapter now caches and serves). It also
asserts h2 ALPN end-to-end (npm's node http2 client).

Current status: **11/11 PASS** (npm, cargo, rubygems, composer, hex, go,
pypi, nuget, helm, conda, rpm).

## Lifecycle harness
`../lifecycle/run.sh` walks five stages per protocol — publish (A) → public
(B installs a well-known upstream package) → private (B installs A's package
from clean caches) → upgrade (A publishes v2, B consumes it) → delete — with
the protocol's REAL client, through the same spoofed sidecar. A and B are
separate processes with isolated HOMEs/caches in one pod.

- Hosted families (**debian/apk/rpm/conda**) publish into a self-published
  repo served by the adapter itself (`/pkgs/<format>/<repo>/...`, no `/hosted`
  segment) and install it with apt/apk/dnf/conda.
- **cargo**, **rubygems**, **pub** treat delete as yank/retract (no
  unpublish in the client).
- Pipelines that need a network bootstrap (composer twine, helm cm-push,
  abuild, rpmbuild, hex archive) install their tooling from the mirror
  first; helm's cm-push plugin is baked into the tool image.

Current status: **18/18 protocols PASS** all five stages.

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
- **nuget**: its layers come off a Southeast-Asia Azure CDN that this cluster's
  egress reaches slowly, so the ~18MB layer pull can time out. It uses the same
  HTTP pull-through path as every other protocol and passes when the CDN is
  fast.
- **swift**: SwiftPM has NO public registry upstream (`api.spm.swift.org` and
  `packages.swift.org` are NXDOMAIN as of 2026-09); its normal resolution path
  is a git clone from GitHub. The script therefore exercises the adapter's
  SCM-to-registry bridge (register a git URL, enumerate tags, serve a source
  archive) through the spoofed proxy.
- **protobuf**: the Buf Schema Registry is a Connect/gRPC service; the `buf`
  CLI talks gRPC directly, so the script drives the adapter's HTTP `/v1` face
  through the spoofed proxy and asserts the response is fetched and served.
