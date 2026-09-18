#!/bin/sh
# Gradle Plugin Portal: a Maven-layout mirror at /m2. Fetch a plugin marker POM.
set -e
CA=/etc/easysidecar/ca.crt
wget --ca-certificate=$CA -qO /tmp/kotlin.pom \
  "https://plugins.gradle.org/m2/org/jetbrains/kotlin/kotlin-stdlib/2.0.0/kotlin-stdlib-2.0.0.pom"
grep -q "<artifactId>kotlin-stdlib</artifactId>" /tmp/kotlin.pom || { echo "gradle: bad pom"; exit 1; }
echo "gradle: plugin portal kotlin-stdlib pom ok"
