#!/bin/sh
set -e
# conanio/conan ships Conan 1.x; the adapter implements the Conan 2.x revision
# protocol. Bootstrap Conan 2 from PyPI (through artifact-pypi via the extra
# rule in conan.rules.yaml). Ask for just the recipe so the test does not need
# every prebuilt binary of a multi-config package.
pip install --no-cache-dir --quiet "conan>=2,<3"
conan profile detect --force >/dev/null 2>&1
conan remote add easylab http://artifact-conan.temp.svc.cluster.local/pkgs/conan --force >/dev/null
conan download zlib/1.3.1 -r easylab --only-recipe 2>/dev/null || conan download zlib/1.3.1 -r easylab
