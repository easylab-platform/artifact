#!/bin/sh
# hex lifecycle: mix hex.publish / fetch public+private / upgrade / retire+delete
set -e
# Erlang's TLS uses its own cacerts file (not SSL_CERT_FILE).
cat /etc/ssl/certs/ca-certificates.crt > /tmp/cacerts.pem 2>/dev/null || true
cat /etc/easyproxy/ca.crt >> /tmp/cacerts.pem
export HEX_CACERTS_PATH=/tmp/cacerts.pem
export HEX_HOME="$HOME/.hex"
export MIX_HOME="$HOME/.mix"
export HEX_API_URL=https://hex.pm
export HEX_API_KEY=lc-token
mix local.hex --force >/dev/null 2>&1
mix local.rebar --force >/dev/null 2>&1
mix archive.install /opt/hex-archive/hex.ez --force >/dev/null 2>&1 || true

APP="lc_probe_${SUFFIX}"
make_pkg() { # $1 = version
  rm -rf "$WORK/pkg"
  mix new "$WORK/pkg" --app "$APP" >/dev/null
  cd "$WORK/pkg"
  cat > mix.exs <<X
defmodule ${APP}.MixProject do
  use Mix.Project
  def project, do: [app: :${APP}, version: "$1", elixir: "~> 1.16", description: "lc probe", package: [licenses: ["MIT"], links: %{}]]
  def application, do: []
end
X
  cat > lib/value.ex <<X
defmodule LcProbe do
  @version "$1"
  def version, do: @version
end
X
}

publish_pkg() { # $1 = version
  make_pkg "$1"
  cd "$WORK/pkg"
  mix hex.publish package --yes
}
