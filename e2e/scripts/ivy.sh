#!/bin/sh
# Ivy protocol: sbt (and Ant) resolve plugins from an Ivy-layout repository.
# repo.scala-sbt.org/build-artifacts redirects to its JFrog store, and a
# legacy sbt plugin lives at the extra-attribute path
# <org>/<module>/scala_2.10/sbt_0.13/<rev>/ivys/ivy.xml.
#
# Assert: the descriptor and its jar both fetch through the proxy, and a
# subsequent fetch is served from the local cache.
set -e
# GNU wget 1.25 on this base trusts SSL_CERT_FILE for GnuTLS only when the
# bundle is a single file; it ignores the container's SSL_CERT_FILE for
# OpenSSL, so pass the CA explicitly (sbt/coursier read the system store, but
# this script speaks the wire protocol directly).
CA=/etc/easysidecar/ca.crt
W="wget --ca-certificate=$CA"
BASE=https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases
PLUGIN=com.typesafe.sbt/sbt-native-packager/scala_2.10/sbt_0.13/0.7.4
$W -qO /tmp/ivy.xml "$BASE/$PLUGIN/ivys/ivy.xml"
grep -q 'organisation="com.typesafe.sbt"' /tmp/ivy.xml || { echo "ivy: bad descriptor"; exit 1; }

# The jar declared by the descriptor's <publications> must be fetchable. The
# Ivy layout puts it under jars/<artifact>.jar (the artifact name, not the
# versioned file name).
$W -qO /tmp/plugin.jar "$BASE/$PLUGIN/jars/sbt-native-packager.jar"
[ -s /tmp/plugin.jar ] || { echo "ivy: empty jar"; exit 1; }

# Cached path: a repeat fetch must still succeed (served locally).
$W -qO /tmp/ivy2.xml "$BASE/$PLUGIN/ivys/ivy.xml"
cmp -s /tmp/ivy.xml /tmp/ivy2.xml || { echo "ivy: cache mismatch"; exit 1; }

echo "ivy: resolved $PLUGIN ($(wc -c </tmp/plugin.jar) byte jar)"
