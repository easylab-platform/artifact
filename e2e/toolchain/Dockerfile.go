ARG BASE_IMAGE=forgejo.develop.10.199.64.20.nip.io/easylab/toolchain-base:latest
FROM ${BASE_IMAGE}
ARG EASYLAB=http://easylab.temp.svc.cluster.local
RUN curl -fsSL -o /tmp/go.tar.gz "${EASYLAB}/pkgs/generic/go/1.27.1/go1.27.1.linux-amd64.tar.gz" \
 && tar -xzf /tmp/go.tar.gz -C /opt && rm /tmp/go.tar.gz
ENV PATH=/opt/go/bin:$PATH
