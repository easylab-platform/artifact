FROM forgejo.develop.10.199.64.20.nip.io/easylab/toolchain-base:latest
ARG EASYLAB=http://easylab.temp.svc.cluster.local
# nix-portable bundles Nix + a bubblewrap-based chroot, so Nix runs on any
# Linux without installing the /nix store system-wide.
RUN mkdir -p /opt/nix-portable \
 && curl -fsSL -o /opt/nix-portable/nix-portable "${EASYLAB}/pkgs/generic/nix-portable/v012/nix-portable-x86_64" \
 && chmod +x /opt/nix-portable/nix-portable \
 && printf '#!/bin/sh\nexec /opt/nix-portable/nix-portable nix "$@"\n' > /usr/local/bin/nix \
 && chmod +x /usr/local/bin/nix
ENV NP_GIT=/usr/bin/git
