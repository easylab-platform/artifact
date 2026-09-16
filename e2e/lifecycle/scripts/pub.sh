#!/bin/sh
# pub lifecycle: dart pub publish / add public+private / upgrade / retract
# (dart has no unpublish; the adapter exposes DELETE /api/packages/<name>.)
set -e
export PUB_CACHE="$HOME/.pub-cache"

make_pkg() { # $1 = version
  mkdir -p "$WORK/pkg/lib"
  cat > "$WORK/pkg/pubspec.yaml" <<EOF
name: ${NAME}
description: lc probe
version: $1
environment:
  sdk: ^3.0.0
EOF
  printf "const packageVersion = '%s';\n" "$1" > "$WORK/pkg/lib/${NAME}.dart"
}

publish_pkg() { # $1 = version
  make_pkg "$1"
  cd "$WORK/pkg"
  dart pub publish --force >/dev/null
}

use_app() { # $1 = constraint, $2 = expected
  mkdir -p "$WORK/app/bin"
  cat > "$WORK/app/pubspec.yaml" <<EOF
name: use
environment:
  sdk: ^3.0.0
dependencies:
  ${NAME}: $1
EOF
  cat > "$WORK/app/bin/main.dart" <<EOF
import 'package:${NAME}/${NAME}.dart';
void main() {
  if (packageVersion != '$2') throw StateError('bad version \$packageVersion');
  print('pub: ${NAME} \$packageVersion');
}
EOF
  cd "$WORK/app"
  dart pub get >/dev/null
  dart run bin/main.dart
}

case "$STAGE" in
publish)
  publish_pkg "$V1"
  echo "pub: published ${NAME}@$V1"
  ;;
public)
  mkdir -p "$WORK/app/bin"
  cat > "$WORK/app/pubspec.yaml" <<'EOF'
name: use
environment:
  sdk: ^3.0.0
dependencies:
  http: ^1.0.0
EOF
  cat > "$WORK/app/bin/main.dart" <<'EOF'
import 'package:http/http.dart' as http;
void main() async {
  final c = http.Client();
  print('pub: public http client ${c.runtimeType}');
}
EOF
  cd "$WORK/app"
  dart pub get >/dev/null
  dart run bin/main.dart
  ;;
private)
  use_app "^${V1}" "$V1"
  ;;
upgrade)
  publish_pkg "$V2"
  use_app "^${V2}" "$V2"
  # The package metadata still lists v1 alongside v2.
  wget -qO- "https://pub.dev/api/packages/${NAME}" | grep -q "\"version\":\"$V2\""
  wget -qO- "https://pub.dev/api/packages/${NAME}" | grep -q "\"$V1\""
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "https://pub.dev/api/packages/${NAME}")"
  [ "$code" = "200" ] || { echo "pub: retract rc=$code"; exit 1; }
  rm -rf "$WORK/app" "$PUB_CACHE"
  if use_app "^${V2}" "$V2" >/dev/null 2>&1; then
    echo "pub: ${NAME} still resolvable after retract"; exit 1
  fi
  echo "pub: retracted ${NAME}"
  ;;
esac
