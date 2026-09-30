# Build the manager binary
FROM --platform=$BUILDPLATFORM docker.io/golang:1.27.1@sha256:e0174e51e81218523251d85d248a90d24c3d5e81543b4f07a5d66229397db190 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
# The builder stage is pinned to BUILDPLATFORM so this always runs natively
# and cross-compiles to TARGETARCH. buildkit still sets TARGETARCH from the
# requested platform, so the binary is for the target; only the Go toolchain
# runs on the host. Without the pin, a linux/riscv64 build runs the golang
# image itself under QEMU, which is slow enough to time the job out.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
ARG TARGETARCH
WORKDIR /
# Create a non-root user with numeric UID
RUN adduser -D -u 65532 -s /bin/sh manager
# Install kubectl and other necessary tools.
# -f matters here: without it curl saves the 404 body as a file called kubectl
# and the image builds green with an XML error document installed as kubectl.
# Upstream publishes the client for a fixed set of platforms only, so a
# TARGETARCH it does not build has to fail this build, not ship broken.
RUN apk add --no-cache curl ca-certificates && \
    curl -fLO "https://dl.k8s.io/release/$(curl -fLs https://dl.k8s.io/release/stable.txt)/bin/linux/${TARGETARCH}/kubectl" && \
    chmod +x kubectl && \
    mv kubectl /usr/local/bin/
COPY --from=builder /workspace/manager .

USER 65532:65532
ENTRYPOINT ["/manager"]
