FROM forgejo.develop.10.199.64.20.nip.io/easylab/toolchain-base:latest
ARG EASYLAB=http://easylab.temp.svc.cluster.local
RUN curl -fsSL -o /tmp/swift.tar.gz "${EASYLAB}/pkgs/generic/swift/6.4.0/swift-6.4.0-RELEASE-debian12.tar.gz" \
 && mkdir -p /opt/swift && tar -xzf /tmp/swift.tar.gz -C /opt/swift --strip-components=1 && rm /tmp/swift.tar.gz
ENV PATH=/opt/swift/usr/bin:$PATH
