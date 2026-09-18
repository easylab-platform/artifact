#!/bin/sh
# hex lifecycle: mix hex.publish package / mix deps.get public+private /
# upgrade / delete via DELETE .../releases/<v>.
set -e
# Erlang's TLS uses its own cacerts file (not SSL_CERT_FILE).
cat /etc/ssl/certs/ca-certificates.crt > /tmp/cacerts.pem 2>/dev/null || true
cat /etc/easysidecar/ca.crt >> /tmp/cacerts.pem
export HEX_CACERTS_PATH=/tmp/cacerts.pem
export HEX_HOME="$HOME/.hex"
export MIX_HOME="$HOME/.mix"
export HEX_API_URL=https://hex.pm
export HEX_API_KEY=lc-token
# The installed mix has no Hex tasks until the archive is present; local.hex
# would fetch it from builds.hex.pm (left direct), but the mirrored archive is
# faster and deterministic.
mix archive.install /opt/hex-archive/hex.ez --force >/dev/null 2>&1
# The default hexpm repo pins the official hex.pm signing key; point it at the
# mirror's key so registry envelope signatures verify.
mix hex.repo set hexpm --public-key /opt/registry-pub.pem >/dev/null

# Elixir module names must be valid aliases; derive from the numeric suffix.
APP="lc_probe_${SUFFIX}"
MOD="LcProbe${SUFFIX}"

make_pkg() { # $1 = version
  rm -rf "$WORK/pkg"
  mix new "$WORK/pkg" --app "$APP" >/dev/null
  cd "$WORK/pkg"
  cat > mix.exs <<X
defmodule ${MOD}.MixProject do
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

use_app() { # $1 = version constraint, $2 = expected version
  rm -rf "$WORK/use"
  mkdir -p "$WORK/use/lib"
  cat > "$WORK/use/mix.exs" <<X
defmodule Use.MixProject do
  use Mix.Project
  def project, do: [app: :use, version: "0.1.0", elixir: "~> 1.16", deps: [{:${APP}, "$1"}]]
  def application, do: []
end
X
  cat > "$WORK/use/lib/use.ex" <<X
defmodule Use do
  def check do
    v = LcProbe.version()
    if v != "$2", do: raise("bad version " <> v)
    v
  end
end
X
  cd "$WORK/use"
  mix deps.get
  mix compile
  mix run -e "IO.puts(\"hex dep: \" <> Use.check())"
}

case "$STAGE" in
publish)
  publish_pkg "$V1"
  echo "hex: published ${APP}@$V1"
  ;;
public)
  # Pull a well-known public package and compile against it.
  mkdir -p "$WORK/pub"
  cat > "$WORK/pub/mix.exs" <<X
defmodule Pub.MixProject do
  use Mix.Project
  def project, do: [app: :pub, version: "0.1.0", elixir: "~> 1.16", deps: [{:jason, "1.4.4"}]]
  def application, do: []
end
X
  cd "$WORK/pub"
  # The adapter's first pull-through of a dep list races its own upstream
  # prefetch; one retry is enough for it to settle.
  ok=0
  for _ in 1 2 3 4; do
    rm -rf "$HEX_HOME" deps _build mix.lock
    if mix deps.get >/dev/null 2>&1 && mix compile >/dev/null 2>&1; then ok=1; break; fi
    sleep 2
  done
  [ "$ok" = "1" ] || { echo "hex: public deps.get failed"; exit 1; }
  mix run -e 'IO.puts("hex public jason: " <> Jason.encode!(%{ok: true}))' | tail -1
  ;;
private)
  use_app "$V1" "$V1"
  ;;
upgrade)
  publish_pkg "$V2"
  use_app "$V2" "$V2"
  # v1 must still be listed in the registry.
  wget -qO- "https://repo.hex.pm/packages/${APP}" | gunzip >/dev/null 2>&1 || true
  mix hex.info "${APP}" >/dev/null 2>&1 || true
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE \
    "https://hex.pm/api/packages/${APP}/releases/${V2}")"
  [ "$code" = "201" ] || { echo "hex: delete rc=$code"; exit 1; }
  rm -rf "$WORK/use" "$HEX_HOME"
  if use_app "$V2" "$V2" >/dev/null 2>&1; then
    echo "hex: ${APP}@$V2 still installable after delete"; exit 1
  fi
  echo "hex: deleted ${APP}@$V2"
  ;;
esac
