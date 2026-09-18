#!/bin/sh
# PECL (pecl.php.net): extension tarball download.
set -e
CA=/etc/easysidecar/ca.crt
wget --ca-certificate=$CA -qO /tmp/redis.tgz "https://pecl.php.net/get/redis"
tar -tzf /tmp/redis.tgz >/dev/null || { echo "pecl: bad tarball"; exit 1; }
echo "pecl: redis tarball ok"
