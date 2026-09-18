#!/bin/sh
# Hackage (Haskell): a plain HTTP tree. Fetch the package index and a package
# tarball through the spoofed proxy (hackage.haskell.org -> artifact-hackage).
set -e
CA=/etc/easysidecar/ca.crt
W="wget --ca-certificate=$CA"
# The 01-index tarball is large (100MB+); fetch only its head via Range so the
# test stays quick while still exercising the pull-through path.
wget --ca-certificate=$CA --header='Range: bytes=0-65535' -qO /tmp/idx "https://hackage.haskell.org/01-index.tar.gz"
[ -s /tmp/idx ] || { echo "hackage: empty index"; exit 1; }
$W -qO /tmp/pkg.tar.gz "https://hackage.haskell.org/package/aeson-2.2.3.0/aeson-2.2.3.0.tar.gz"
tar -tzf /tmp/pkg.tar.gz >/dev/null || { echo "hackage: bad tarball"; exit 1; }
echo "hackage: index + aeson-2.2.3.0.tar.gz ok"
