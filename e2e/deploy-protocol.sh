#!/usr/bin/env bash
# Deploy one artifact Deployment+Service per protocol into the e2e namespace.
# Each deployment mounts exactly one protocol (isolation: its upstream fetches
# never traverse another protocol's sidecar). The Service name is artifact-<p>.
set -euo pipefail
NS="${NS:-temp}"
IMAGE="${IMAGE:-forgejo.develop.10.199.64.20.nip.io/easylab/artifact:v0.1.5}"
UPSTREAM_PROXY="${UPSTREAM_PROXY:-http://mihomo.develop.svc.cluster.local:7890}"
PROTOCOLS=(${PROTOCOLS:-npm pypi go cargo maven debian apk rpm oci})

for p in "${PROTOCOLS[@]}"; do
  name="artifact-${p}"
  extra=""
  extra_args=""
  if [ "$p" = "oci" ]; then
    extra=$'        - name: ARTIFACT_MAX_BODY\n          value: "34359738368"\n'
  fi
  if [ "$p" = "rpm" ]; then
    # The Fedora client baseurl is https://dl.fedoraproject.org/pub/fedora/
    # linux/releases/40/Everything/x86_64/os/; the proxy preserves that path,
    # and the rpm adapter treats the first segment ("pub") as the repo key and
    # fetches "<base>/<rest>", so the base is .../pub.
    extra_args=', "--upstreams=rpm=https://dl.fedoraproject.org/pub"'
  fi
  cat <<YAML | kubectl apply -n "${NS}" -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  labels: { app: artifact-e2e, protocol: "${p}" }
spec:
  replicas: 1
  strategy: { type: Recreate }
  selector: { matchLabels: { app: ${name} } }
  template:
    metadata: { labels: { app: ${name} } }
    spec:
      containers:
      - name: artifact
        image: ${IMAGE}
        args: ["--listen=:8080", "--data=/data", "--protocols=${p}", "--self-base=http://${name}.${NS}.svc.cluster.local"$extra_args]
        ports: [{ name: http, containerPort: 8080 }]
        resources:
          requests: { cpu: 50m, memory: 64Mi }
          limits:   { cpu: 500m, memory: 512Mi }
        env:
        - name: HTTP_PROXY
          value: "${UPSTREAM_PROXY}"
        - name: HTTPS_PROXY
          value: "${UPSTREAM_PROXY}"
        - name: NO_PROXY
          value: "localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io"
${extra}        readinessProbe:
          tcpSocket: { port: http }
          initialDelaySeconds: 2
          periodSeconds: 5
        livenessProbe:
          tcpSocket: { port: http }
          initialDelaySeconds: 10
          periodSeconds: 15
        volumeMounts: [{ name: data, mountPath: /data }]
      volumes: [{ name: data, emptyDir: {} }]
---
apiVersion: v1
kind: Service
metadata:
  name: ${name}
  labels: { app: artifact-e2e, protocol: "${p}" }
spec:
  selector: { app: ${name} }
  ports: [{ name: http, port: 80, targetPort: http }]
YAML
done
echo "deployed: ${PROTOCOLS[*]}"
