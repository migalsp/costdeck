# syntax=docker/dockerfile:1

# The dashboard is static files, so it is built once on the build host whatever the
# target platform is.
FROM --platform=$BUILDPLATFORM node:24-alpine AS ui
WORKDIR /src/ui
COPY ui/package.json ui/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY ui/ ./
RUN npm run build

# Go cross-compiles natively: building on $BUILDPLATFORM keeps multi-arch images out of
# QEMU emulation, which made arm64 builds several times slower.
FROM --platform=$BUILDPLATFORM golang:1.26 AS manager
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
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=manager /out/manager /manager
USER 65532:65532

ENTRYPOINT ["/manager"]
