#!/bin/sh
set -e
mkdir -p /w && cd /w
cat > pom.xml <<'X'
<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion><groupId>t</groupId><artifactId>t</artifactId><version>1.0</version>
<dependencies><dependency><groupId>org.slf4j</groupId><artifactId>slf4j-api</artifactId><version>2.0.9</version></dependency></dependencies>
</project>
X
keytool -importcert -noprompt -alias easylab -file /etc/easyproxy/ca.crt -keystore /tmp/ts.p12 -storetype PKCS12 -storepass changeit >/dev/null 2>&1 || true
JAVA_TOOL_OPTIONS="-Djavax.net.ssl.trustStore=/tmp/ts.p12 -Djavax.net.ssl.trustStorePassword=changeit -Djavax.net.ssl.trustStoreType=PKCS12" \
  mvn -q -B -Dmaven.repo.local=/tmp/m2 org.apache.maven.plugins:maven-dependency-plugin:3.6.1:resolve
