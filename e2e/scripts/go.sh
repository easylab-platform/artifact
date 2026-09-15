#!/bin/sh
set -e
mkdir -p /w && cd /w
go mod init t >/dev/null 2>&1 || true
GOFLAGS=-mod=mod GOSUMDB=off go get golang.org/x/text@v0.14.0
