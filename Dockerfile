# Build the React UI
FROM node:20 AS ui-builder
WORKDIR /app
COPY ui/package*.json ./ui/
RUN cd ui && npm ci
COPY ui/ ./ui/
RUN cd ui && npm run build

# Build the Go Manager
FROM golang:1.25 AS builder
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
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Copy the built React UI into the internal/api/ui directory for go:embed
COPY --from=ui-builder /app/ui/dist internal/api/ui

# Build
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -ldflags "-X github.com/migalsp/costdeck-operator/internal/api.Version=${VERSION}" -a -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
