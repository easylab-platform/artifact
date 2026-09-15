# syntax=docker/dockerfile:1
# artifact: multi-protocol package registry (pull-through + hosted).
#
# Built WITH the cluster buildkitd: run ./build-image.sh (targets the shared
# buildkitd, then pushes to forgejo via skopeo). No local build daemon.
#
# Build context: the artifact repo root (go.work + every protocol module +
# cmd/). All sibling modules resolve through the replace directives in
# cmd/go.mod (no network for them); third-party deps (modernc sqlite,
# ulikunitz/xz) resolve from GOPROXY through the build proxy.
ARG REGISTRY=forgejo.develop.10.199.64.20.nip.io/root
ARG ALPINE=3.24

# ---- build ----
FROM ${REGISTRY}/golang:1.26-alpine AS build
ARG HTTP_PROXY=http://mihomo.develop.svc.cluster.local:7890
ARG HTTPS_PROXY=http://mihomo.develop.svc.cluster.local:7890
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=localhost,127.0.0.1,.svc.cluster.local,.svc,.nip.io,10.199.64.20,.develop.10.199.64.20.nip.io \
    GOPROXY=https://proxy.golang.org \
    GOWORK=off \
    CGO_ENABLED=0
RUN apk add --no-cache ca-certificates git
WORKDIR /src
COPY . .
# cmd/ is the server module: with GOWORK=off its replace directives resolve
# every sibling protocol module from the copied tree; third-party deps come
# from GOPROXY over the build proxy.
RUN cd cmd && go build -mod=mod -trimpath -ldflags="-s -w" -o /out/artifact .

# ---- runtime ----
FROM ${REGISTRY}/alpine:${ALPINE}
RUN apk add --no-cache ca-certificates curl
# /data holds the sqlite metadata + CAS blobs (mount a PVC/hostPath there).
COPY --from=build /out/artifact /usr/local/bin/artifact
ENV EASYVCS_HOME=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/artifact"]
CMD ["server", "--listen=:8080", "--data=/data"]
