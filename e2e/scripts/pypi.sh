#!/bin/sh
set -e
pip install --no-cache-dir --disable-pip-version-check six
# Import/run assertion: execute code from the installed package.
python3 -c 'import six; assert six.__version__; print("six", six.__version__)'
