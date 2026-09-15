#!/bin/sh
set -e
mkdir -p /w && cd /w
cat > pom.xml <<'X'
<project xmlns="http://maven.apache.org/POM/4.0.0"><modelVersion>4.0.0</modelVersion><groupId>t</groupId><artifactId>t</artifactId><version>1.0</version>
<dependencies><dependency><groupId>org.slf4j</groupId><artifactId>slf4j-api</artifactId><version>2.0.9</version></dependency></dependencies>
</project>
X
cat > Main.java <<'X'
public class Main {
  public static void main(String[] a) {
    System.out.println("java + slf4j: " + org.slf4j.LoggerFactory.class.getName());
  }
}
X
keytool -importcert -noprompt -alias easylab -file /etc/easyproxy/ca.crt -keystore /tmp/ts.p12 -storetype PKCS12 -storepass changeit >/dev/null 2>&1 || true
export JAVA_TOOL_OPTIONS="-Djavax.net.ssl.trustStore=/tmp/ts.p12 -Djavax.net.ssl.trustStorePassword=changeit -Djavax.net.ssl.trustStoreType=PKCS12"
# Resolve the dependency through the proxy, then compile+run against it.
mvn -q -B -Dmaven.repo.local=/tmp/m2 org.apache.maven.plugins:maven-dependency-plugin:3.6.1:resolve
CP="/tmp/m2/org/slf4j/slf4j-api/2.0.9/slf4j-api-2.0.9.jar"
javac -cp "$CP" Main.java
java -cp ".:$CP" Main
