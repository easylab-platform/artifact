#!/bin/sh
# Google Maven (Android): a Maven-layout mirror. Fetch an Android artifact POM
# + AAR through the spoofed proxy (dl.google.com -> artifact-google/maven).
set -e
CA=/etc/easysidecar/ca.crt
W="wget --ca-certificate=$CA -qO"
$W /tmp/gradle.pom "https://dl.google.com/dl/android/maven2/com/android/tools/build/gradle/8.7.0/gradle-8.7.0.pom"
grep -q "<artifactId>gradle</artifactId>" /tmp/gradle.pom || { echo "google: bad pom"; exit 1; }
$W /tmp/core.aar "https://dl.google.com/dl/android/maven2/androidx/core/core/1.13.0/core-1.13.0.aar"
[ -s /tmp/core.aar ] || { echo "google: empty aar"; exit 1; }
echo "google: gradle pom + androidx core aar ok"
