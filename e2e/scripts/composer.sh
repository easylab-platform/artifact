#!/bin/sh
set -e
cd /tmp
composer config -g secure-http false
mkdir -p /w && cd /w
composer require monolog/monolog --no-interaction --no-progress
# Run assertion: load the installed library via the generated autoloader.
php -r 'require "/w/vendor/autoload.php"; $l=new Monolog\Logger("t"); echo "php + monolog: ", get_class($l), "\n";'
