#!/bin/sh
set -e
apt-get update -o Acquire::Retries=0
apt-get install -y --no-install-recommends ca-certificates jq
