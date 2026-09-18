#!/bin/sh
set -e
# Dart's pub honors the system trust store; add the egress CA and let the
# spoofed proxy intercept pub.dev.
cat /etc/easysidecar/ca.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
dart pub cache add http --version 1.2.2
# Compile/run assertion: build a small program against a cached package.
rm -rf /w && mkdir -p /w/bin
cat > /w/pubspec.yaml <<'X'
name: w
environment:
  sdk: ">=3.0.0 <4.0.0"
dependencies:
  path: any
X
cat > /w/bin/main.dart <<'X'
import 'package:path/path.dart' as p;
void main() => print('dart + path: ' + p.join('a', 'b'));
X
cd /w
dart pub get >/dev/null
dart run bin/main.dart
