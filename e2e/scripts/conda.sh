#!/bin/sh
set -e
# miniconda refuses the public defaults channels unless their ToS is accepted.
conda tos accept --override-channels --channel https://repo.anaconda.com/pkgs/main >/dev/null 2>&1 || true
conda tos accept --override-channels --channel https://repo.anaconda.com/pkgs/r >/dev/null 2>&1 || true
conda create -y -n t --override-channels -c defaults python zlib
# Run assertion: execute python from the created env and import the package.
conda run -n t python -c 'import zlib; print("conda + zlib:", zlib.ZLIB_VERSION)'
