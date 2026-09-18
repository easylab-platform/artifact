#!/bin/sh
# JitPack: a Maven-layout mirror that builds GitHub repos on demand. JitPack
# only serves artifacts it has built; master-SNAPSHOT for a known repo works.
set -e
CA=/etc/easysidecar/ca.crt
wget --ca-certificate=$CA -qO /tmp/jitpack.pom \
  "https://jitpack.io/com/github/jitpack/maven-simple/master-SNAPSHOT/maven-simple-master-SNAPSHOT.pom"
grep -q "maven-simple" /tmp/jitpack.pom || { echo "jitpack: bad pom"; exit 1; }
echo "jitpack: maven-simple master-SNAPSHOT pom ok"
