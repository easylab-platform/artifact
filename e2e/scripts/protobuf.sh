#!/bin/sh
set -e
# buf.build is a Connect/gRPC service, so the buf CLI never uses an HTTP pull
# face. Drive the adapter's HTTP /v1 face through the spoofed proxy (buf.build
# is MITM'd and add_prefix /artifacts/protobuf) and assert it fetches + serves the
# module response.
wget -qO /tmp/mod "https://buf.build/buf.build/googleapis/googleapis/v1/modules/buf.build/googleapis/googleapis" 2>/dev/null || \
  curl -fsS -o /tmp/mod "https://buf.build/buf.build/googleapis/googleapis/v1/modules/buf.build/googleapis/googleapis"
echo "module response bytes: $(wc -c </tmp/mod)"
test -s /tmp/mod
# Compile assertion: `buf build` validates a proto module locally.
mkdir -p /w/proto && cd /w
cat > buf.yaml <<'X'
version: v2
modules:
  - path: proto
X
cat > proto/t.proto <<'X'
syntax = "proto3";
package t;
message T { string s = 1; int32 n = 2; }
X
buf build
echo "protobuf: buf build ok"
