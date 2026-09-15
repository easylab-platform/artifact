#!/bin/sh
set -e
# Erlang's TLS uses its own cacerts file (not SSL_CERT_FILE).
cat /etc/ssl/certs/ca-certificates.crt > /tmp/cacerts.pem 2>/dev/null || true
cat /etc/easyproxy/ca.crt >> /tmp/cacerts.pem
export HEX_CACERTS_PATH=/tmp/cacerts.pem
mix local.rebar --force >/dev/null 2>&1 || true
mix archive.install /opt/hex-archive/hex.ez --force >/dev/null
# Create a project whose dependency is fetched from the proxy, then compile+run
# it (mix deps.get pulls jason through artifact-hex).
rm -rf /w && mix new /w --app w >/dev/null
cd /w
cat > mix.exs <<'X'
defmodule W.MixProject do
  use Mix.Project
  def project, do: [app: :w, version: "0.1.0", elixir: "~> 1.16", deps: [{:jason, "1.4.4"}]]
  def application, do: [extra_applications: [:logger]]
end
X
mix deps.get >/dev/null
mix compile >/dev/null
mix run -e 'IO.puts("elixir + jason: " <> Jason.encode!(%{ok: true}))'
