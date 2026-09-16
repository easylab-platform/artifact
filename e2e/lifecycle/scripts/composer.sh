#!/bin/sh
# composer lifecycle: API publish (PUT /api/packages) with an archive built by
# `composer archive` / real `composer install` for public+private / upgrade /
# delete via DELETE /api/packages/<name>.
set -e
cat /etc/easyproxy/ca.crt >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null || true
export HOME="/tmp/lc-home-${STAGE}"
export COMPOSER_HOME="$HOME/.composer"
export COMPOSER_ALLOW_SUPERUSER=1
mkdir -p "$HOME"
REPO=https://repo.packagist.org
PKG="easylab/lc-probe-${SUFFIX}"
SHORT="lc-probe-${SUFFIX}"

archive_pkg() { # $1 = version
  rm -rf "$WORK/pkg" "$WORK/dist"
  mkdir -p "$WORK/pkg/src"
  cd "$WORK/pkg"
  cat > composer.json <<X
{
  "name": "${PKG}",
  "description": "lc probe",
  "type": "library",
  "version": "$1",
  "license": "MIT",
  "autoload": {"psr-4": {"Easylab\\\\LcProbe\\\\": "src/"}}
}
X
  cat > src/Probe.php <<X
<?php
namespace Easylab\\LcProbe;
final class Probe { public const VERSION = "$1"; }
X
  composer archive --format=zip --dir="$WORK/dist" >/dev/null 2>&1
  # `composer archive` names the file "<vendor>-<name>-<version>.zip"
  zip="$(ls "$WORK/dist"/*.zip | head -1)"
  echo "$zip"
}

publish_pkg() { # $1 = version
  zip="$(archive_pkg "$1")"
  code="$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary "@$zip" "$REPO/api/packages")"
  [ "$code" = "201" ] || [ "$code" = "200" ] || { echo "composer: publish rc=$code"; exit 1; }
}

install_pkg() { # $1 = constraint, $2 = expected version
  rm -rf "$WORK/proj"
  mkdir -p "$WORK/proj"
  cd "$WORK/proj"
  cat > composer.json <<X
{
  "name": "easylab/use",
  "repositories": [{"type": "composer", "url": "$REPO"}],
  "require": {"${PKG}": "$1"}
}
X
  ok=0
  for _ in 1 2 3; do
    rm -rf vendor composer.lock "$COMPOSER_HOME/cache"
    if composer install --no-interaction --no-progress >/dev/null 2>&1; then ok=1; break; fi
    sleep 2
  done
  [ "$ok" = "1" ] || { echo "composer: install ${PKG}@$1 failed"; exit 1; }
  got="$(php -r 'require "vendor/autoload.php"; echo \Easylab\LcProbe\Probe::VERSION;')"
  [ "$got" = "$2" ] || { echo "composer: got $got want $2"; exit 1; }
  echo "composer: ${PKG} $got ok"
}

case "$STAGE" in
publish)
  publish_pkg "$V1"
  echo "composer: published ${PKG}@$V1"
  ;;
public)
  rm -rf "$WORK/proj"
  mkdir -p "$WORK/proj"
  cd "$WORK/proj"
  cat > composer.json <<X
{
  "name": "easylab/use-public",
  "repositories": [{"type": "composer", "url": "$REPO"}],
  "require": {"monolog/monolog": "^3.0"}
}
X
  # The adapter's first pull-through of packagist metadata can race its own
  # upstream prefetch on a cold cache; retry with a clean composer cache.
  ok=0
  for _ in 1 2 3; do
    rm -rf vendor composer.lock "$COMPOSER_HOME/cache"
    if composer install --no-interaction --no-progress >/dev/null 2>&1; then ok=1; break; fi
    sleep 2
  done
  [ "$ok" = "1" ] || { echo "composer: public install failed"; exit 1; }
  php -r 'require "vendor/autoload.php"; echo "composer public monolog: ", Monolog\Logger::class, PHP_EOL;' 
  ;;
private)
  install_pkg "$V1" "$V1"
  ;;
upgrade)
  publish_pkg "$V2"
  install_pkg "$V2" "$V2"
  # Both versions are advertised by the p2 provider.
  prov="$(curl -s "$REPO/p2/${PKG}.json")"
  echo "$prov" | grep -q "\"$V1\"" || { echo "composer: $V1 dropped"; exit 1; }
  echo "$prov" | grep -q "\"$V2\"" || { echo "composer: $V2 missing"; exit 1; }
  echo "composer: upgraded to $V2, both listed"
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "$REPO/api/packages/${PKG}")"
  [ "$code" = "200" ] || { echo "composer: delete rc=$code"; exit 1; }
  rm -rf "$WORK/proj" "$COMPOSER_HOME/cache"
  if install_pkg "$V2" "$V2" >/dev/null 2>&1; then
    echo "composer: $V2 still installable after delete"; exit 1
  fi
  echo "composer: deleted ${PKG}"
  ;;
esac
