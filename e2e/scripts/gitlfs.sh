#!/bin/sh
# Git LFS: clone a small public LFS repository through the mirror. The git
# smart-HTTP objects come from the bare mirror; the LFS batch + object come
# from the git adapter's LFS endpoints (object fetched through from the
# upstream and cached in the CAS on the first pull).
set -e
export GIT_TERMINAL_PROMPT=0 GIT_LFS_SKIP_SMUDGE=0
CA=/etc/easysidecar/ca.crt
export GIT_SSL_CAINFO=$CA SSL_CERT_FILE=$CA
rm -rf /w
# GIT_LFS_* make lfs use the same CA.
export GIT_LFS_INSECURE=0
git clone --quiet https://github.com/Schoonology/git-lfs-test.git /w
# The LFS object must have been materialized (not left as a pointer).
head -c 3 /w/binary.jpg | od -An -tx1 | grep -qi 'ff d8 ff' || { echo "gitlfs: binary.jpg not materialized"; exit 1; }
echo "gitlfs: clone + lfs object ok ($(wc -c </w/binary.jpg) bytes)"
