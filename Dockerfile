# Dockerfile
# Build stage
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w" \
    -o manager cmd/main.go

# Runtime stage - distroless, non-root, no shell
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
