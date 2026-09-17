#!/bin/sh
# LuaRocks: a plain HTTP tree. `luarocks install` reads the manifest and then
# the rock, both rewritten to the gateway.
set -e
CA=/etc/easyproxy/ca.crt
export SSL_CERT_FILE=$CA CURL_CA_BUNDLE=$CA
# The client resolves luarocks.org; the manifest is version-specific (-5.4).
luarocks --lua-version=5.4 install luafilesystem --tree=/tmp/lr-tree >/dev/null 2>&1
[ -n "$(find /tmp/lr-tree -name 'luafilesystem*.so' 2>/dev/null)" ] || \
  [ -n "$(find /tmp/lr-tree -iname '*luafilesystem*' 2>/dev/null)" ] || {
    echo "luarocks: luafilesystem not installed"; exit 1; }
echo "luarocks: luafilesystem installed"
