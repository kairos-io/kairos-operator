# Build the manager binary
FROM --platform=$BUILDPLATFORM docker.io/golang:1.27.1@sha256:3680233e3204827fbdc66088528ae6d4b3d034f51d03a99d454f6de034888244 AS builder
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
WORKDIR /
# Create a non-root user with numeric UID
RUN adduser -D -u 65532 -s /bin/sh manager
# ca-certificates lets the manager talk to https apiservers and OCI
# registries. nsenter, which the reboot Pod uses for the host reboot, ships with
# alpine's busybox and does not need to be installed separately.
RUN apk add --no-cache ca-certificates
COPY --from=builder /workspace/manager .

USER 65532:65532
ENTRYPOINT ["/manager"]
