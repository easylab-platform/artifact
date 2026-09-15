#!/bin/sh
set -e
cd /tmp
dotnet new console -o w >/dev/null
cd w
dotnet add package Newtonsoft.Json --version 13.0.3 >/dev/null
dotnet restore
