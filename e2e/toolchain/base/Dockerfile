# Shared base for the protocol-client images: debian slim plus the base
# utilities, shared libraries and system package managers apt provides. Every
# /opt language toolchain is layered on top by a Dockerfile.<proto>.
#
# The suite is detected from the base image (bookworm | trixie) so the same
# Dockerfile builds either; the package set differs (t64 ABI, libicu76, ...).
#
# deb.debian.org is throttled to ~7KB/s from the build containers, so the apt
# sources (deb822 format in /etc/apt/sources.list.d/debian.sources) are pointed
# at a fast in-region mirror (USTC) over plain HTTP. APT_PROXY can optionally
# route apt via mihomo instead (it 502s intermittently, so it is not default).
ARG BASE_SLIM=forgejo.develop.10.199.64.20.nip.io/root/debian:trixie-slim
FROM ${BASE_SLIM}

ARG APT_PROXY=
ARG APT_MIRROR=mirrors.ustc.edu.cn
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8

RUN set -eux; \
    . /etc/os-release; \
    case "${VERSION_CODENAME:-bookworm}" in \
      trixie) SUITES="trixie trixie-updates"; SEC="trixie-security"; \
              ICU=libicu76; SSL=libssl3t64; RL=libreadline8t64;; \
      *)      SUITES="bookworm bookworm-updates"; SEC="bookworm-security"; \
              ICU=libicu72; SSL=libssl3; RL=libreadline8;; \
    esac; \
    printf 'Types: deb\nURIs: http://%s/debian\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n\nTypes: deb\nURIs: http://%s/debian-security\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' \
        "$APT_MIRROR" "$SUITES" "$APT_MIRROR" "$SEC" > /etc/apt/sources.list.d/debian.sources; \
    if [ -n "$APT_PROXY" ]; then export http_proxy=$APT_PROXY https_proxy=$APT_PROXY; fi; \
    apt-get -o Acquire::Retries=10 update; \
    for i in 1 2 3 4 5 6; do \
      apt-get -o Acquire::Retries=10 install -y --no-install-recommends \
        ca-certificates curl wget xz-utils unzip zip bzip2 \
        binutils file git openssh-client procps jq less python3 \
        gcc g++ make libc6-dev pkg-config \
        "$ICU" "$SSL" "$RL" libyaml-0-2 libffi8 libgmp10 \
        libncurses6 zlib1g liblzma5 libzstd1 gnupg bind9-dnsutils \
        libatomic1 \
      && break || { echo "apt retry $i"; sleep 3; }; \
    done; \
    rm -rf /var/lib/apt/lists/*; \
    # Restore the official sources: at RUNTIME the spoofed proxy redirects
    # deb.debian.org to the local artifact-debian mirror (fast), and the
    # debian protocol test asserts exactly that path.
    printf 'Types: deb\nURIs: http://deb.debian.org/debian\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n\nTypes: deb\nURIs: http://deb.debian.org/debian-security\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' \
        "$SUITES" "$SEC" > /etc/apt/sources.list.d/debian.sources

WORKDIR /root
CMD ["sleep", "infinity"]
