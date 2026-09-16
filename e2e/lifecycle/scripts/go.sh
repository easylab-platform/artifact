#!/bin/sh
# go lifecycle: module PUT /upload (proxy-style upload) / real `go get` for
# public+private / upgrade / delete via DELETE /upload.
set -e
cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
export HOME="/tmp/lc-home-${STAGE}"
export GOPATH="$HOME/go"
export GOMODCACHE="$GOPATH/pkg/mod"
export GOFLAGS=-mod=mod
export GOSUMDB=off
export GONOSUMDB='*'
export GOPROXY=https://proxy.golang.org,direct
mkdir -p "$HOME"
MOD="example.com/lc-probe-${SUFFIX}"

build_zip() { # $1 = version; echoes the zip path
  src="$WORK/src-${1}"
  zdir="$WORK/zip-${1}"
  rm -rf "$src" "$zdir"
  mkdir -p "$src"
  cat > "$src/go.mod" <<X
module ${MOD}

go 1.21
X
  cat > "$src/probe.go" <<X
package lcprobe

const Version = "$1"
X
  # A proxy module zip is rooted at "<module>@<version>/".
  mkdir -p "$zdir/$MOD@$1"
  cp "$src"/go.mod "$src"/probe.go "$zdir/$MOD@$1/"
  ( cd "$zdir" && zip -qr "$WORK/${SUFFIX}-$1.zip" "$MOD@$1" )
  echo "$WORK/${SUFFIX}-$1.zip"
}

publish_mod() { # $1 = version
  z="$(build_zip "$1")"
  code="$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary "@$z" \
    "https://proxy.golang.org/upload?name=${MOD}&version=$1")"
  [ "$code" = "201" ] || [ "$code" = "200" ] || { echo "go: upload rc=$code"; exit 1; }
}

use_mod() { # $1 = version, $2 = expected
  rm -rf "$WORK/use"
  mkdir -p "$WORK/use"
  cd "$WORK/use"
  cat > go.mod <<X
module example.com/use

go 1.21

require ${MOD} $1
X
  cat > main.go <<X
package main

import (
	"fmt"

	lcprobe "${MOD}"
)

func main() { fmt.Println("go: " + lcprobe.Version) }
X
  go mod tidy >/dev/null 2>&1
  out="$(go run . 2>&1 | tail -1)"
  echo "$out" | grep -q "$2" || { echo "go: got '$out' want $2"; exit 1; }
  echo "go: ${MOD} $2 ok"
}

case "$STAGE" in
publish)
  publish_mod "$V1"
  echo "go: published ${MOD}@$V1"
  ;;
public)
  rm -rf "$WORK/pub"
  mkdir -p "$WORK/pub"
  cd "$WORK/pub"
  cat > go.mod <<X
module example.com/pubuse

go 1.21
X
  cat > main.go <<'X'
package main

import (
	"fmt"

	"golang.org/x/text/language"
)

func main() { fmt.Println("go public: " + language.English.String()) }
X
  go mod tidy >/dev/null 2>&1
  go run . 2>&1 | tail -1
  ;;
private)
  use_mod "$V1" "$V1"
  ;;
upgrade)
  publish_mod "$V2"
  use_mod "$V2" "$V2"
  # @v/list still advertises both versions.
  list="$(curl -s "https://proxy.golang.org/${MOD}/@v/list")"
  echo "$list" | grep -q "$V1" || { echo "go: $V1 dropped"; exit 1; }
  echo "$list" | grep -q "$V2" || { echo "go: $V2 missing"; exit 1; }
  echo "go: upgraded to $V2, both listed"
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
    "https://proxy.golang.org/upload?name=${MOD}&version=$V2")"
  [ "$code" = "200" ] || { echo "go: delete rc=$code"; exit 1; }
  rm -rf "$GOMODCACHE/cache/download/example.com" "$WORK/use"
  if use_mod "$V2" "$V2" >/dev/null 2>&1; then
    echo "go: $V2 still resolvable after delete"; exit 1
  fi
  echo "go: deleted ${MOD}@$V2"
  ;;
esac
