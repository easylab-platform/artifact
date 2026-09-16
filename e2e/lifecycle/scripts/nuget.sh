#!/bin/sh
# nuget lifecycle: dotnet pack+push / restore public+private / upgrade / unlist
set -e
SRV=https://api.nuget.org/v3/index.json
mkdir -p "$HOME/.nuget/NuGet"
cat > "$HOME/.nuget/NuGet/NuGet.Config" <<'X'
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="easylab" value="https://api.nuget.org/v3/index.json" />
  </packageSources>
</configuration>
X

pack_and_push() { # $1 = version
  rm -rf "$WORK/pkg"
  mkdir -p "$WORK/pkg"
  cat > "$WORK/pkg/pkg.csproj" <<EOF
<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <PackageId>${NAME}</PackageId>
    <Version>$1</Version>
    <TargetFramework>net10.0</TargetFramework>
    <PackageOutputPath>\$(MSBuildProjectDirectory)/nupkg</PackageOutputPath>
    <GeneratePackageOnBuild>false</GeneratePackageOnBuild>
  </PropertyGroup>
</Project>
EOF
  cat > "$WORK/pkg/Probe.cs" <<EOF
namespace Easylab.Lc;
public static class Probe {
  public const string Version = "$1";
}
EOF
  dotnet pack "$WORK/pkg/pkg.csproj" -v q --nologo >/dev/null
  dotnet nuget push "$WORK/pkg/nupkg/${NAME}.$1.nupkg" \
    -s "$SRV" --api-key lc-token --skip-duplicate >/dev/null
}

consume_app() { # $1 = version, $2 = expected const
  rm -rf "$WORK/app"
  dotnet new console -o "$WORK/app" --no-restore -v q >/dev/null
  cd "$WORK/app"
  dotnet add package "$NAME" --version "$1" >/dev/null
  cat > Program.cs <<EOF
using Easylab.Lc;
var v = Probe.Version;
if (v != "$2") throw new System.Exception("bad version " + v);
System.Console.WriteLine("nuget: ${NAME} " + v);
EOF
  dotnet run -v q --nologo
}

case "$STAGE" in
publish)
  pack_and_push "$V1"
  echo "nuget: pushed ${NAME}@$V1"
  ;;
public)
  rm -rf "$WORK/app"
  dotnet new console -o "$WORK/app" --no-restore -v q >/dev/null
  cd "$WORK/app"
  dotnet add package Newtonsoft.Json --version 13.0.3 >/dev/null
  dotnet build -v q --nologo >/dev/null
  ls "$HOME/.nuget/packages/newtonsoft.json/13.0.3" >/dev/null
  echo "nuget: public Newtonsoft.Json 13.0.3 ok"
  ;;
private)
  consume_app "$V1" "$V1"
  ;;
upgrade)
  pack_and_push "$V2"
  consume_app "$V2" "$V2"
  idx="$(wget -qO- "https://api.nuget.org/v3/flatcontainer/$(echo "$NAME" | tr 'A-Z' 'a-z')/index.json")"
  echo "$idx" | grep -q "\"$V1\"" || { echo "nuget: $V1 dropped from index"; exit 1; }
  echo "$idx" | grep -q "\"$V2\"" || { echo "nuget: $V2 missing from index"; exit 1; }
  ;;
delete)
  dotnet nuget delete "$NAME" "$V2" -s "$SRV" --api-key lc-token \
    --non-interactive >/dev/null
  rm -rf "$HOME/.nuget/packages" "$WORK/app"
  if dotnet new console -o "$WORK/app" --no-restore -v q >/dev/null 2>&1 \
     && cd "$WORK/app" \
     && dotnet add package "$NAME" --version "$V2" >/dev/null 2>&1; then
    echo "nuget: $V2 still restorable after delete"; exit 1
  fi
  echo "nuget: unlisted ${NAME}@$V2"
  ;;
esac
