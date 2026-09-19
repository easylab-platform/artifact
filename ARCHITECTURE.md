# artifact — architecture

`artifact` is a multi-protocol package registry: it mirrors public ecosystems
(npm, PyPI, Maven, OCI, ...) through a cache, serves operator-published
("hosted") packages, and can transparently fetch arbitrary HTTP(S) assets.
One process mounts many protocols; embedding it (easylab) mounts all of them.

This document is the map. It names the five concepts and the seams between
them, then gives a "which API do I call" table so an adapter author never has
to guess.

## The five concepts

```
┌────────────┐   wire format      ┌─────────────────────────────────────────┐
│  Protocol  │  (maven layout,    │  /artifacts/<target-id>/<native-path>   │
│  adapter   │   npm packument,   │  or /v2/... for OCI                     │
│            │   OCI /v2)         │                                         │
└────────────┘                    └─────────────────────────────────────────┘
       │ builds against                        ▲ routed by target
       ▼                                        │
┌────────────────────────────────────────────────────────────────────────┐
│ Registry = Blobs + Meta + Upstreams + Owners + TargetStore             │
└────────────────────────────────────────────────────────────────────────┘
   │            │                    │
   ▼            ▼                    ▼
┌──────┐  ┌─────────┐  ┌──────────────────────────────┐
│ Blob │  │  Meta   │  │ Upstreams                     │
│ (CAS)│  │ (index) │  │  = targets (identity+policy)  │
│ by   │  │ (format,│  │  + overrides/proxy/airgap     │
│ sha  │  │  repo,  │  │  + fetch/remote flows         │
│ 256  │  │  ver)   │  │                               │
└──────┘  └─────────┘  └──────────────────────────────┘
```

| Concept | Type | Meaning |
|---|---|---|
| **Protocol** | a package under `/artifacts/<name>/` implementing `artifactkit.Protocol` | The *wire shape*: how a client speaks (maven's `group/artifact/version/file`, npm's packument JSON, OCI's `/v2` manifest flow). Registered by `Register(name, NewHandler)` in `init()`. |
| **Target** | `targets.Target` (`targets` package) | The *upstream identity*: a base URL, the hostnames a client may address it by, how an inbound path maps to the mount, auth policy. The single source of truth for routing + the egress policy. |
| **Repository** | `RepoScope` (request context) | The *content boundary* of one protocol: `(namespace, name)`. Namespace comes from the format's `NamespaceResolver` (npm scope, maven groupId, OCI host). Isolated targets prefix storage with `t:<id>`. |
| **Artifact** | `Artifact` (`core`) | The *indexed row*: `(Format, Repository, Version)` → media type + descriptors pointing at CAS blobs. Held by `IndexStore` (GORM: sqlite/postgres/mysql). |
| **Blob** | `BlobStore` (`core`) | The *immutable bytes*, content-addressed by `sha256:<hex>`. Filesystem default; S3 via the `s3blob` module's registry hook. |

Everything else is mechanics on top of these.

## The target model (routing + egress in one place)

`targets.Builtins()` is the compile-time table; `targets.Registry` overlays
operator-declared targets. Each `Target` carries:

- `ID` / `Protocol` — `maven` (default target) or `maven.google` (named mirror)
- `Kind` — `Mirror` (coordinates are global; mirrors share storage) or
  `Identity` (the host is part of identity: `ghcr.io/acme/app` ≠ `docker.io/acme/app`)
- `Base` / `Hosts` / `PathPrefix` / `Aux` / `ExtraStrips` / `DirectHosts`
- `Auth` — `passthrough` (forward the client's credential) | `basic` | `bearer`

Two derived artifacts, both **never hand-written**:

- **Egress policy** — `targets.EgressPolicy()` / `RenderEgressYAML()` derive the
  sidecar rewrite rules from the same table. Adding a target adds its routing,
  so inbound mapping and outbound base cannot drift.
- **SSRF allow-list** — the hosts a target declares are exactly the hosts the
  host-driven resolution path accepts (`Upstreams.HostBase`).

## Request lifecycle

```
client → easysidecar (egress) → TargetDispatcher
  1. split /artifacts/<target-id>/... ; resolve the target (Registry)
  2. build a RepoScope (namespace/name/host/proto) via the format's resolver
  3. rewrite the path to /artifacts/<protocol>/<native-path>
  4. adapter.ServeHTTP:
       a. parse native path → (repo, version, filename)
       b. read Meta (cache hit) → ServeBlob / ServeCachedBlob
       c. miss → Registry.Fetch* / Remote(...) → resolve base (priority below)
       d. store bytes in Blobs, index in Meta, serve
```

Upstream resolution priority (`resolveBase`), highest first:

1. explicit per-repository override (`Upstreams.Repos`)
2. the mounted target (`/artifacts/maven.google/...` beats the request host)
3. the origin the client dialed (`X-Forwarded-Host`, allow-listed)
4. the format's default target

## The adapter API — which call do I use?

Adapters hold one `*artifactkit.Registry`. There are **two** upstream entry
points and **five** fetch flows; pick by intent:

### Upstream handles — `Registry.Remote(ctx, UpstreamSpec)`

One constructor for every way an adapter reaches an upstream:

| Intent | Spec |
|---|---|
| absolute base the adapter resolved itself | `{Base: url}` |
| address a source by hostname (git/ivy mirrors) | `{Format: "git", Host: host}` |
| a sub-endpoint on its own host (cargo index, nuget search) | `{Format: "cargo", Sub: "index"}` |
| one repository of a format (override + target auth) | `{Format: "npm", Repo: name}` |
| the request-context priority (override → origin → default) | `{Format: "npm"}` |

Then call `remote.Get`, `remote.Do`, `remote.GetBytes`, `GetBytesFollow`, etc.

### Fetch flows — pick by shape

| Intent | Call | Result |
|---|---|---|
| small metadata doc (index page, packument, maven pom) | `Registry.FetchPath(ctx, format, path)` | `Fetched` (bytes in memory, indexed) |
| same, but follow 3xx (Ivy, JitPack, tree roots) | `Registry.FetchPathFollow(ctx, format, path)` | `Fetched` |
| large / Range-addressable file (tree index, layer, jar) | `Registry.FetchToBlob(ctx, format, path)` | `(digest, size, ok)` — streamed to CAS |
| the path-tree protocols with TTL + revalidation | `Registry.FetchCachedPath(ctx, ...)` | `PathCacheResult` |
| an arbitrary absolute URL (netcache, presigned href) | `Registry.FetchURIToBlob(ctx, url, opts)` | `URICacheResult` |
| store local bytes (publish / after an adapter fetch) | `Registry.StoreStream(ctx, r)` / `StoreAndHash(ctx, b)` | `Stored` |

Serve results with `ServeBlob` / `ServeBlobAt(Named)` / `ServeCachedBlob` /
`ServeData` — these are the only response paths (they own Range/HEAD/conditional
semantics and header replay).

## Storage seams

- `IndexStore` (`core/store.go`): `Put/Get/Delete/ListVersions/ListArtifacts/
  ListRepositories*/ListPackages/ReferencedDigests/...`. The `scopedStore`
  decorator (`core/scoped_store.go`) applies the request's `RepoScope` so
  adapters keep writing unscoped `(format, repository, version)`.
- `BlobStore`: `Stat/Open/PutIfAbsent/HashesFor/Delete/List` (+ optional
  `HashPersister` for the O(1) checksum sidecar). Backends register via
  `store.RegisterBlobBackend("s3", ...)` from their own module.

## Auth in one paragraph

`Auth` resolves a request to a principal. `TokenAuth` (static table) and
`StoreAuth` (database) both mint OCI bearer tokens and understand every
protocol's credential scheme (Bearer / bare token / Basic / `X-NuGet-ApiKey`).
`AuthorizeWrite`/`AuthorizeWriteFor` gate publishing (write-level credential +
optional `Ownership` tenancy); `AuthorizeReadFor` hides private repos;
`AuthorizeAdmin` gates the operator surface. `auth == nil` or an open instance
(no users) permits write — dev mode.

## Module layout

- `core` — substrate: stores, fetch/remote flows, scope, auth, hosted handlers.
- `targets` — stdlib-only target table + derived egress (imported by core,
  easylab and easysidecar; must not pull heavy deps).
- one module per protocol adapter (`npm`, `pypi`, `oci`, `httpcache`, ...),
  plus `netcache` (arbitrary URL cache), `generic` (raw store), `system`
  (operator admin API) and `s3blob` (blob backend).
- `cmd` — the server binary; blank-imports adapters and mounts them.
- `e2e` — the regression harness (`test-all.sh`).

## Conventions

- **One concept, one name.** "upstream" is a target; "remote" is a resolved
  handle; "fetch" is an operation. Don't reintroduce per-case constructors —
  extend `UpstreamSpec`.
- **Derive, don't duplicate.** Routing, egress and the SSRF allow-list all come
  from `targets`. A host or base is never listed by hand in two places.
- **The response path owns HTTP semantics.** Never write artifact bytes
  directly; call a `Serve*` helper.
- **Stream large bodies.** `StoreStream`/`FetchToBlob` write to a temp file;
  never `io.ReadAll` a package upload.
