#!/bin/sh
set -e
# Native Nix image: exercise the CLI and the binary-cache client path through
# the spoofed proxy. Nix has its own CA handling, so trust the egress CA via
# NIX_SSL_CERT_FILE.
export NIX_CONFIG="experimental-features = nix-command flakes"
export NIX_SSL_CERT_FILE=/etc/easyproxy/ca.crt
nix --version
nix store info --store https://cache.nixos.org
