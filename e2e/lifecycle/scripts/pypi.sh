#!/bin/sh
# pypi lifecycle: twine upload / pip install public+private / upgrade / delete
# (pypi has no client-side delete; the adapter exposes DELETE /api/projects).
set -e

make_project() { # $1 = version
  mkdir -p "$WORK/proj/src/lc_probe_${SUFFIX}"
  cat > "$WORK/proj/pyproject.toml" <<EOF
[build-system]
requires = ["setuptools>=68"]
build-backend = "setuptools.build_meta"

[project]
name = "lc-probe-${SUFFIX}"
version = "$1"

[tool.setuptools.packages.find]
where = ["src"]
EOF
  printf '__version__ = "%s"\n' "$1" > "$WORK/proj/src/lc_probe_${SUFFIX}/__init__.py"
}

publish_project() { # $1 = version
  make_project "$1"
  cd "$WORK/proj"
  rm -rf dist
  # Bootstrap the real publishing toolchain through the proxy itself.
  pip install --quiet --disable-pip-version-check build twine setuptools >/dev/null
  python -m build --no-isolation --wheel --sdist >/dev/null
  twine upload --non-interactive --repository-url https://pypi.org/upload \
    -u lc -p lc-token dist/* >/dev/null
}

case "$STAGE" in
publish)
  publish_project "$V1"
  echo "pypi: published lc-probe-${SUFFIX}@$V1"
  ;;
public)
  pip install --quiet --no-cache-dir --disable-pip-version-check six >/dev/null
  python -c 'import six; assert six.__version__; print("pypi: public six", six.__version__)'
  ;;
private)
  pip install --quiet --no-cache-dir --disable-pip-version-check "lc-probe-${SUFFIX}==${V1}" >/dev/null
  python -c "import lc_probe_${SUFFIX} as m; assert m.__version__=='${V1}', m.__version__"
  echo "pypi: private lc-probe-${SUFFIX}@$V1 ok"
  ;;
upgrade)
  publish_project "$V2"
  pip install --quiet --no-cache-dir --disable-pip-version-check --upgrade "lc-probe-${SUFFIX}" >/dev/null
  python -c "import lc_probe_${SUFFIX} as m; assert m.__version__=='${V2}', m.__version__"
  pip index versions "lc-probe-${SUFFIX}" | grep -q "$V1"
  echo "pypi: upgraded to $V2, $V1 still listed"
  ;;
delete)
  code="$(curl -s -o /dev/null -w '%{http_code}' -X DELETE "https://pypi.org/api/projects/lc-probe-${SUFFIX}")"
  [ "$code" = "200" ] || { echo "pypi: delete rc=$code"; exit 1; }
  if pip download --no-deps --no-cache-dir --disable-pip-version-check \
       -d "$WORK/dl" "lc-probe-${SUFFIX}==${V2}" >/dev/null 2>&1; then
    echo "pypi: still downloadable after delete"; exit 1
  fi
  echo "pypi: deleted lc-probe-${SUFFIX}"
  ;;
esac
