# Per-protocol client toolchain images

Each protocol in the e2e matrix gets its **own** image with a single client
toolchain, rather than one giant all-in-one image.

## Pieces
- `base/Dockerfile` → `easylab/toolchain-base:{latest,trixie}`
  Debian slim + base utilities / shared libs / build tools (apt), suite
  auto-detected from the base image (bookworm or trixie; the package set
  differs: t64 ABI, libicu76, ...). The only step that needs public internet;
  apt is pointed at a fast in-region mirror during the build, then the sources
  are restored to `deb.debian.org` so the `debian` protocol test can assert the
  proxy path.
- `Dockerfile.<proto>` → `easylab/tool-<proto>:{latest,trixie}`
  The Debian protocols are `FROM easylab/toolchain-base` and add exactly one
  toolchain under /opt, fetched from easylab's generic store so the build needs
  no egress proxy. Three protocols use their **native distro base** (the client
  package manager must be the real one) and are the same for both variants:
  - `rpm` → `root/fedora:44` (native dnf/rpm)
  - `apk` → `root/alpine:3.24` (native apk)
  - `nix` → `root/nix:2.35.2` (native Nix + /nix store)
- `manifest.txt` — every toolchain artifact (name|version|file|url-or-LOCAL).
- `mirror.sh` — fetch each artifact (once, via the dev-box proxy) and PUT it
  into easylab `/artifacts/generic/<name>/<ver>/<file>`; idempotent.
- `build-one.sh <proto|base> [tag]` — buildkit → skopeo → forgejo for one image.
- `build-all.sh` — builds every protocol image in sequence (base must exist).

## Variants: bookworm (default) and trixie
`build-one.sh`/`build-all.sh` take `VARIANT=bookworm|trixie` (bookworm tags
`:latest`, trixie tags `:trixie`):
```sh
EASYLAB=http://<easylab-ip> ARTIFACT_TOKEN=devtoken ./mirror.sh
VARIANT=bookworm ./build-one.sh base && VARIANT=bookworm ./build-all.sh
VARIANT=trixie   ./build-one.sh base && VARIANT=trixie   ./build-all.sh
```
rpm/apk/nix (native Fedora/Alpine/Nix bases) are built only in the bookworm
variant (their tags are suite-independent). Run the matrix with
`TOOL_TAG=trixie ./run.sh`.

## Compile assertions
Every `scripts/<proto>.sh` not only fetches through the proxy but also
**builds/runs code against the fetched artifact** (npm require, `go build`,
`cargo run`, `javac`+`java`, `dotnet run`, `swiftc`, `mix run`, `dart run`,
`php` with the library autoloader, `nix-instantiate`, `jq` on installed
output, …), so a toolchain that cannot actually compile fails the matrix.

## Rebuild from scratch
```sh
EASYLAB=http://<easylab-ip> ARTIFACT_TOKEN=devtoken ./mirror.sh
./build-one.sh base
./build-all.sh
```

## Versions (2026-09-15)
node 26.8.2 · go 1.27.1 · rust 1.98.1 · python 3.14.7 + uv 0.12.14 ·
temurin jdk 25.0.4.1 · maven 3.9.16 · dotnet 10.0.401 · ruby 4.0.7 ·
php 8.5.10 · composer 2.10.3 · erlang OTP 29.0.6 · elixir 1.20.4 ·
dart 3.13.3 · helm 4.3.0 · buf 1.73.0 · conan 2.32.0 · skopeo 1.24.0 ·
regctl 0.11.6 · miniconda latest · nix-portable v012 ·
apk-tools-static 3.0.8 · ruby via ruby-builder · swift 6.4.0

## Notes / gotchas
- `rubygems`: the ruby-builder prebuilt bakes `/opt/hostedtoolcache/...` into
  its shebangs; it is unpacked at exactly that path.
- `hex`: the OTP tarball has no `bin/`; its `Install -minimal` creates it. A
  copy of the hex archive is baked in (`/opt/hex-archive`) for offline
  bootstrap; the test then uses `mix deps.get/compile/run`.
- `composer`: PHP is compiled from source (php-src 8.5.10) on the Debian base;
  its build-only toolchain is purged and the runtime shared libs (libzip5
  (trixie) / libzip4 (bookworm), libonig5, ...) are kept. composer.phar is then
  added.
- `swift`: Swift ships per-suite toolchains; the image picks debian12 on
  bookworm / debian13 on trixie and installs libc6-dev + stdlib so swiftc can
  link. The test drives the adapter's SCM-to-registry bridge (no public
  registry upstream) and then compiles/runs a Foundation program.
- `nix`: the nixos/nix image trusts its own CA set; the test sets
  `NIX_SSL_CERT_FILE` to the egress CA. (Nix 2.20+ is the `nix` CLI; there is
  no `nix-store` binary.)
- `debian`: apt on trixie (apt 3.0) resolves via its own method and bypasses
  the spoofed resolver, so the test pins `Acquire::http::Proxy=http://127.0.0.1:80`
  (the sidecar) explicitly; bookworm steers by spoofed DNS alone.
- The matrix manifest sets `imagePullPolicy: Always`: tool images are re-pushed
  under the same tag, and a stale node cache would otherwise run old layers.
