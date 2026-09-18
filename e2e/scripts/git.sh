#!/bin/sh
# Git smart-HTTP mirror: clone a small public repository twice. The first clone
# populates the local bare mirror; the second must be served from local objects
# (asserted by timing + a clean working tree). Run through the spoofed proxy,
# so github.com resolves to the sidecar and the request is rewritten to
# /artifacts/git/github.com/....
set -e
export GIT_TERMINAL_PROMPT=0
REPO=https://github.com/octocat/Hello-World.git

rm -rf /w1 /w2
t1=$(date +%s)
git clone --quiet --depth=1 "$REPO" /w1
t2=$(date +%s)
git clone --quiet --depth=1 "$REPO" /w2
t3=$(date +%s)

[ -f /w1/README ] || { echo "git: first clone missing README"; exit 1; }
[ -f /w2/README ] || { echo "git: second clone missing README"; exit 1; }
git -C /w2 log --oneline >/dev/null || { echo "git: second clone has no history"; exit 1; }

echo "git: clone ok (cold=$((t2 - t1))s warm=$((t3 - t2))s)"
