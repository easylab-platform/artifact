#!/bin/sh
# opam repository (opam.ocaml.org): index tarball + a package's opam file.
set -e
CA=/etc/easysidecar/ca.crt
W="wget --ca-certificate=$CA -qO"
wget --ca-certificate=$CA --header='Range: bytes=0-65535' -qO /tmp/opam-idx "https://opam.ocaml.org/index.tar.gz"
[ -s /tmp/opam-idx ] || { echo "opam: empty index"; exit 1; }
$W /tmp/opam-pkg "https://opam.ocaml.org/packages/dune/dune.3.17.2/opam"
grep -q 'opam-version' /tmp/opam-pkg || { echo "opam: bad opam file"; exit 1; }
echo "opam: index + dune opam ok"
