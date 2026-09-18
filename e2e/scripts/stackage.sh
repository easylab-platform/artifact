#!/bin/sh
# Stackage (stackage.org): snapshot config + a package page from a snapshot.
set -e
CA=/etc/easysidecar/ca.crt
W="wget --ca-certificate=$CA -qO"
$W /tmp/lts.yaml "https://stackage.org/lts/cabal.config"
grep -qi 'constraints' /tmp/lts.yaml || { echo "stackage: bad cabal.config"; exit 1; }
echo "stackage: lts cabal.config ok"
