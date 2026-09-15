#!/bin/sh
echo "nix: exercising the binary-cache proxy"
nix-store --version
nix-store --realise /nix/store/00000000000000000000000000000000-hello.drv 2>&1 || true
