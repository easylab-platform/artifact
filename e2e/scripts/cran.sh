#!/bin/sh
# CRAN (R): a plain HTTP tree. The real client is Rscript's install.packages()
# against cran.r-project.org, rewritten to the gateway by the sidecar.
set -e
CA=/etc/easysidecar/ca.crt
export CURL_CA_BUNDLE=$CA SSL_CERT_FILE=$CA
d=/tmp/cran-lib
rm -rf "$d" && mkdir -p "$d"
Rscript -e "install.packages('jsonlite', lib='$d', repos='https://cran.r-project.org', quiet=TRUE, dependencies=FALSE)" >/dev/null 2>&1
[ -d "$d/jsonlite" ] || { echo "cran: jsonlite not installed"; exit 1; }
Rscript -e ".libPaths('$d'); library(jsonlite); cat(sprintf('{\"ok\":%s}\n', toJSON(TRUE)))"
