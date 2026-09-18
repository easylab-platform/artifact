#!/bin/sh
# Clojars: a Maven-layout mirror. Fetch a Clojure artifact POM + jar.
set -e
CA=/etc/easysidecar/ca.crt
wget --ca-certificate=$CA -qO /tmp/clj.pom "https://repo.clojars.org/ring/ring-core/1.11.0/ring-core-1.11.0.pom"
grep -q "<artifactId>ring-core</artifactId>" /tmp/clj.pom || { echo "clojars: bad pom"; exit 1; }
wget --ca-certificate=$CA -qO /tmp/clj.jar "https://repo.clojars.org/ring/ring-core/1.11.0/ring-core-1.11.0.jar"
[ -s /tmp/clj.jar ] || { echo "clojars: empty jar"; exit 1; }
echo "clojars: ring-core pom + jar ok"
