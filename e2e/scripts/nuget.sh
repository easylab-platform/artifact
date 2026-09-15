#!/bin/sh
set -e
# The adapter's service index advertises plain-HTTP resource URLs (the
# in-cluster registry), which NuGet 10 rejects unless explicitly allowed.
mkdir -p /root/.nuget/NuGet
cat > /root/.nuget/NuGet/NuGet.Config <<'X'
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="easylab" value="https://api.nuget.org/v3/index.json" allowInsecureConnections="true" />
  </packageSources>
</configuration>
X
cd /tmp
rm -rf w
dotnet new console -o w >/dev/null
cd w
dotnet add package Newtonsoft.Json --version 13.0.3 >/dev/null
dotnet restore
# Compile/run assertion: use the restored package for real.
cat > Program.cs <<'X'
using Newtonsoft.Json;
var o = JsonConvert.DeserializeObject<Dictionary<string,int>>("{\"a\":1}");
System.Console.WriteLine("dotnet + json: " + o["a"]);
X
dotnet run --verbosity quiet
