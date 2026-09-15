# Per-protocol client toolchain images

Each protocol in the e2e matrix gets its **own** small image with a single
client toolchain, rather than one giant all-in-one image.

## Pieces
- `base/Dockerfile` → `easylab/toolchain-base:latest`
  Debian 12 bookworm-slim + base utilities / shared libs (apt). The only step
  that needs public internet; apt is pointed at a fast in-region mirror during
  the build, then the sources are restored to `deb.debian.org` so the `debian`
  protocol test can assert the spoofed proxy path.
- `Dockerfile.<proto>` → `easylab/tool-<proto>:latest`
  Each downloads exactly one toolchain (to `/opt`) from easylab's **generic
  store**, so the build needs no egress proxy. Exceptions use dedicated bases:
  - `rpm`  → `root/fedora:44` (dnf/rpm native)
  - `apk`  → `root/alpine:3.24` (apk native)
  - `nix`  → `root/nix:2.35.2` (nix native)
  - `composer` builds on the official `php:8.5.10-cli-bookworm` image (a
    Debian base; the official composer image is Alpine).
- `manifest.txt` — every toolchain artifact (name|version|file|url-or-LOCAL).
- `mirror.sh` — fetch each artifact (once, via the dev-box proxy) and PUT it
  into easylab `/pkgs/generic/<name>/<ver>/<file>`; idempotent.
- `build-one.sh <proto|base> [tag]` — buildkit → skopeo → forgejo for one image.
- `build-all.sh` — builds every protocol image in sequence (base must exist).

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
  copy of the hex archive is baked in (`/opt/hex-archive`) for offline use.
- `composer` runs on the official php:8.5.10-cli-bookworm image + unzip/git
  (composer extracts dist zips); it no longer uses static-php-cli, whose phar
  handling segfaulted in this container runtime.
- `swift` has no public registry upstream (api.spm.swift.org is NXDOMAIN); its
  test drives the adapter's SCM-to-registry bridge.
