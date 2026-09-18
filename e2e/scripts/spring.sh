#!/bin/sh
# Spring repo: a Maven-layout mirror. The /release path requires auth (401);
# the /milestone repo is public, so exercise that path through the mirror.
set -e
CA=/etc/easysidecar/ca.crt
wget --ca-certificate=$CA -qO /tmp/spring.pom \
  "https://repo.spring.io/milestone/org/springframework/spring-core/6.2.0-M1/spring-core-6.2.0-M1.pom"
grep -q "<artifactId>spring-core</artifactId>" /tmp/spring.pom || { echo "spring: bad pom"; exit 1; }
echo "spring: milestone spring-core pom ok"
