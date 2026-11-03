# syntax=docker/dockerfile:1

# The dashboard is static files, so it is built once on the build host whatever the
# target platform is.
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY ui/ ./
RUN npm run build

# Go cross-compiles natively: building on $BUILDPLATFORM keeps multi-arch images out of
# QEMU emulation, which made arm64 builds several times slower.
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS manager
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# Set GO_BUILD_JOBS=1 on memory-constrained builders (for example Docker Desktop with a
# 2 GiB VM); the default uses every core.
ARG GO_BUILD_JOBS=
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
COPY --from=ui /src/ui/dist/ internal/api/ui/
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build ${GO_BUILD_JOBS:+-p $GO_BUILD_JOBS} -trimpath \
      -ldflags "-s -w -X github.com/migalsp/costdeck-operator/internal/api.Version=${VERSION}" \
      -o /out/manager ./cmd

# distroless/static ships no tzdata; cmd/main.go embeds it via time/tzdata.
FROM gcr.io/distroless/static:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
WORKDIR /
COPY --from=manager /out/manager /manager
USER 65532:65532

ENTRYPOINT ["/manager"]
