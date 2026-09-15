#!/bin/sh
set -e
# static-php-cli's build is cwd-sensitive (segfaults outside /tmp); run from /tmp.
cd /tmp
composer config -g secure-http false
mkdir -p /w && cd /w
composer require monolog/monolog --no-interaction --no-progress
