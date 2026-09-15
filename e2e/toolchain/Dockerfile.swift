ARG BASE_IMAGE=forgejo.develop.10.199.64.20.nip.io/easylab/toolchain-base:latest
FROM ${BASE_IMAGE}
ARG EASYLAB=http://easylab.temp.svc.cluster.local
ARG APT_MIRROR=mirrors.ustc.edu.cn
# Swift ships per-suite toolchains; pick the one matching the base (bookworm ->
# debian12, trixie -> debian13) and install the runtime + compile dependencies
# so swiftc can actually link (libc6-dev provides Scrt1.o/crti.o).
RUN set -eux; \
    . /etc/os-release; \
    case "${VERSION_CODENAME:-bookworm}" in \
      trixie) SUITES="trixie trixie-updates"; SEC="trixie-security"; SWIFTDIR=6.4.0-debian13; SWIFTSUITE=debian13; CURL=libcurl4t64; CXXDEV=libstdc++-14-dev;; \
      *)      SUITES="bookworm bookworm-updates"; SEC="bookworm-security"; SWIFTDIR=6.4.0; SWIFTSUITE=debian12; CURL=libcurl4; CXXDEV=libstdc++-12-dev;; \
    esac; \
    printf 'Types: deb\nURIs: http://%s/debian\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n\nTypes: deb\nURIs: http://%s/debian-security\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' "$APT_MIRROR" "$SUITES" "$APT_MIRROR" "$SEC" > /etc/apt/sources.list.d/debian.sources; \
    apt-get -o Acquire::Retries=10 update; \
    for i in 1 2 3 4 5 6; do \
      apt-get -o Acquire::Retries=10 install -y --no-install-recommends \
        libc6-dev binutils "$CURL" libedit2 libncurses6 libsqlite3-0 \
        libxml2 libz3-4 tzdata zlib1g-dev libpython3-dev "$CXXDEV" \
      && break || { echo "retry $i"; sleep 3; }; \
    done; \
    curl -fsSL -o /tmp/swift.tar.gz "${EASYLAB}/pkgs/generic/swift/${SWIFTDIR}/swift-6.4.0-RELEASE-${SWIFTSUITE}.tar.gz"; \
    mkdir -p /opt/swift && tar -xzf /tmp/swift.tar.gz -C /opt/swift --strip-components=1 && rm /tmp/swift.tar.gz; \
    rm -rf /var/lib/apt/lists/*; \
    printf 'Types: deb\nURIs: http://deb.debian.org/debian\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n\nTypes: deb\nURIs: http://deb.debian.org/debian-security\nSuites: %s\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' "$SUITES" "$SEC" > /etc/apt/sources.list.d/debian.sources
ENV PATH=/opt/swift/usr/bin:$PATH
