#!/usr/bin/env bash
# Generate the fixed dev-only egress MITM CA used by the e2e harness.
#
# The CA is deterministic-per-run only in the sense that it is generated ONCE
# and then reused; it is a throwaway development trust root (never ship it).
# re-run this script (then `kubectl apply` the secret) to rotate.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NS="${NS:-temp}"
SECRET="${SECRET:-artifact-e2e-ca}"

mkdir -p "${DIR}/ca"
if [ ! -f "${DIR}/ca/ca.crt" ]; then
  OPENSSL_CONF=/dev/null openssl genrsa -out "${DIR}/ca/ca.key" 3072
  OPENSSL_CONF=/dev/null openssl req -x509 -new -nodes -key "${DIR}/ca/ca.key" -sha256 \
    -days 3650 -subj "/CN=artifact-e2e-dev-ca/O=easylab" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign,digitalSignature" \
    -out "${DIR}/ca/ca.crt"
fi

kubectl create secret generic "${SECRET}" -n "${NS}" \
  --from-file=ca.crt="${DIR}/ca/ca.crt" --from-file=ca.key="${DIR}/ca/ca.key" \
  --dry-run=client -o yaml | kubectl apply -f -
echo "CA ready: ${DIR}/ca/ca.crt (secret ${NS}/${SECRET})"
