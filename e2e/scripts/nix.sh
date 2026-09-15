#!/bin/sh
set -e
# nix-portable runs Nix 2.20 (the `nix` CLI; nix-store is a subcommand now).
# Exercise the CLI + the binary-cache client path.
export NP_GIT=/usr/bin/git
nix --version
nix store info --store https://cache.nixos.org 2>&1 | head -2 || true
