#!/bin/sh
set -e
composer config -g secure-http false
mkdir -p /w && cd /w
composer require monolog/monolog --no-interaction --no-progress
