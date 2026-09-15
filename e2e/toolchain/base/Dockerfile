# Shared base for the protocol-client images: debian 12 bookworm-slim plus the
# base utilities, shared libraries and system package managers apt provides.
# Every /opt language toolchain is layered on top by a Dockerfile.<proto>.
#
# deb.debian.org is throttled to ~7KB/s from the build containers, so the apt
# sources (deb822 format in /etc/apt/sources.list.d/debian.sources) are pointed
# at a fast in-region mirror (USTC) over plain HTTP. APT_PROXY can optionally
# route apt via mihomo instead (it 502s intermittently, so it is not default).
ARG BASE_SLIM=forgejo.develop.10.199.64.20.nip.io/root/debian:bookworm-slim
FROM ${BASE_SLIM}

ARG APT_PROXY=
ARG APT_MIRROR=mirrors.ustc.edu.cn
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8

RUN set -eux; \
    printf 'Types: deb\nURIs: http://%s/debian\nSuites: bookworm bookworm-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n\nTypes: deb\nURIs: http://%s/debian-security\nSuites: bookworm-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' \
        "$APT_MIRROR" "$APT_MIRROR" > /etc/apt/sources.list.d/debian.sources; \
    if [ -n "$APT_PROXY" ]; then export http_proxy=$APT_PROXY https_proxy=$APT_PROXY; fi; \
    apt-get -o Acquire::Retries=10 update; \
    for i in 1 2 3 4 5 6; do \
      apt-get -o Acquire::Retries=10 install -y --no-install-recommends \
        ca-certificates curl wget xz-utils unzip zip bzip2 \
        binutils file git openssh-client procps jq less python3 \
        libicu72 libssl3 libyaml-0-2 libffi8 libreadline8 libgmp10 \
        libncurses6 zlib1g liblzma5 libzstd1 gnupg dnsutils \
        libatomic1 \
      && break || { echo "apt retry $i"; sleep 3; }; \
    done; \
    rm -rf /var/lib/apt/lists/*; \
    # Restore the official sources: at RUNTIME the spoofed proxy redirects
    # deb.debian.org to the local artifact-debian mirror (fast), and the
    # debian protocol test asserts exactly that path.
    printf 'Types: deb\nURIs: http://deb.debian.org/debian\nSuites: bookworm bookworm-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n\nTypes: deb\nURIs: http://deb.debian.org/debian-security\nSuites: bookworm-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' \
        > /etc/apt/sources.list.d/debian.sources

WORKDIR /root
CMD ["sleep", "infinity"]
