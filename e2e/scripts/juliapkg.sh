#!/bin/sh
# Julia: the package server is a plain HTTP tree (pkg.julialang.org/registries,
# /meta, then <uuid>/<treehash> registry and artifact tarballs). Point
# JULIA_PKG_SERVER at the gateway and let Pkg do a registry + package install.
set -e
CA=/etc/easysidecar/ca.crt
export SSL_CERT_FILE=$CA
export JULIA_PKG_SERVER=https://pkg.julialang.org
export JULIA_DEPOT_PATH=/tmp/julia-depot
d=$(mktemp -d)
cd "$d"
julia -e 'using Pkg; Pkg.Registry.add("General"); Pkg.add("JSON2")' >/dev/null 2>&1
julia --project=. -e 'using JSON2; println("julia: JSON2 loaded")'
