#!/bin/sh
# buf.build's BSR is a Connect/gRPC service: the buf CLI talks gRPC to
# buf.build directly, so it never exercises an HTTP pull-through face. Drive
# the adapter's HTTP /v1 face through the spoofed proxy (buf.build is MITM'd
# and add_prefix /pkgs/protobuf) and assert the module response is fetched and
# served by the adapter.
set -e
wget -qO /tmp/mod --header='Accept: application/json' \
  "https://buf.build/buf.build/googleapis/googleapis/v1/modules/buf.build/googleapis/googleapis"
echo "module response bytes: $(wc -c </tmp/mod)"
test -s /tmp/mod
