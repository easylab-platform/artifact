#!/bin/sh
set -e
mkdir -p /w && cd /w
go mod init t >/dev/null 2>&1 || true
GOFLAGS=-mod=mod GOSUMDB=off go get golang.org/x/text@v0.14.0
# Compile/run assertion: build a binary that uses the fetched module.
cat > main.go <<'X'
package main

import (
	"fmt"
	"golang.org/x/text/language"
)

func main() { fmt.Println("go + x/text:", language.English) }
X
go build -o /w/app .
/w/app
