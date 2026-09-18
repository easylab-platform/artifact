#!/bin/sh
# maven lifecycle: mvn deploy / dependency:get public+private / upgrade / DELETE
# (maven has no client delete; the adapter exposes DELETE on the artifact path.)
set -e
# Java ignores SSL_CERT_FILE; point it at a truststore holding the egress CA.
keytool -importcert -noprompt -alias easylab -file /etc/easysidecar/ca.crt \
  -keystore "$HOME/ts.p12" -storetype PKCS12 -storepass changeit >/dev/null 2>&1 || true
export JAVA_TOOL_OPTIONS="-Djavax.net.ssl.trustStore=$HOME/ts.p12 -Djavax.net.ssl.trustStorePassword=changeit -Djavax.net.ssl.trustStoreType=PKCS12"
G=io.easylab.lc
A="lc-probe-${SUFFIX}"

deploy_artifact() { # $1 = version
  rm -rf "$WORK/proj"
  mkdir -p "$WORK/proj/src/main/java/io/easylab/lc"
  cat > "$WORK/proj/pom.xml" <<EOF
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>${G}</groupId>
  <artifactId>${A}</artifactId>
  <version>$1</version>
  <packaging>jar</packaging>
  <properties>
    <maven.compiler.release>21</maven.compiler.release>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding>
  </properties>
  <distributionManagement>
    <repository>
      <id>easylab</id>
      <url>https://repo.maven.apache.org/maven2</url>
    </repository>
  </distributionManagement>
</project>
EOF
  cat > "$WORK/proj/src/main/java/io/easylab/lc/LcProbe.java" <<EOF
package io.easylab.lc;
public final class LcProbe {
  public static final String VERSION = "$1";
  public static String version() { return VERSION; }
}
EOF
  mvn -q -B -Dmaven.repo.local="$HOME/m2" -f "$WORK/proj/pom.xml" deploy -DskipTests
}

repo_path="$HOME/m2/$(echo "$G" | tr . /)/$A"

case "$STAGE" in
publish)
  deploy_artifact "$V1"
  echo "maven: deployed ${G}:${A}:$V1"
  ;;
public)
  mvn -q -B -Dmaven.repo.local="$HOME/m2" dependency:get -Dartifact=junit:junit:4.13.2
  ls "$HOME"/m2/junit/junit/4.13.2/junit-4.13.2.jar >/dev/null
  echo "maven: public junit 4.13.2 ok"
  ;;
private)
  mvn -q -B -Dmaven.repo.local="$HOME/m2" dependency:get -Dartifact="${G}:${A}:${V1}"
  ls "$repo_path/$V1/$A-$V1.jar" >/dev/null
  echo "maven: private ${A}:$V1 ok"
  ;;
upgrade)
  deploy_artifact "$V2"
  mvn -q -B -Dmaven.repo.local="$HOME/m2" dependency:get -Dartifact="${G}:${A}:${V2}"
  ls "$repo_path/$V2/$A-$V2.jar" >/dev/null
  # maven-metadata.xml lists both versions.
  wget -qO- "https://repo.maven.apache.org/maven2/$(echo "$G" | tr . /)/$A/maven-metadata.xml" \
    | grep -q "<version>$V1</version>"
  wget -qO- "https://repo.maven.apache.org/maven2/$(echo "$G" | tr . /)/$A/maven-metadata.xml" \
    | grep -q "<version>$V2</version>"
  echo "maven: upgraded to $V2, metadata lists both"
  ;;
delete)
  base="https://repo.maven.apache.org/maven2/$(echo "$G" | tr . /)/$A"
  for v in "$V1" "$V2"; do
    code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$base/$v/$A-$v.jar")"
    [ "$code" = "200" ] || { echo "maven: delete $v jar rc=$code"; exit 1; }
    curl -s -o /dev/null -X DELETE "$base/$v/$A-$v.pom"
  done
  if mvn -q -B -Dmaven.repo.local="$HOME/m2" dependency:get -Dartifact="${G}:${A}:${V1}" >/dev/null 2>&1; then
    echo "maven: $V1 still resolvable after delete"; exit 1
  fi
  echo "maven: deleted ${A} {$V1,$V2}"
  ;;
esac
