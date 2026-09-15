#!/bin/sh
set -e
# The spoofed proxy rewrites deb.debian.org to artifact-debian; the base image
# restored the official sources for exactly this test.
apt-get update -o Acquire::Retries=0
apt-get install -y --no-install-recommends ca-certificates jq
