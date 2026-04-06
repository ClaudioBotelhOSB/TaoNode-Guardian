# Dockerfile
# Build stage — single-platform linux/amd64
FROM golang:1.24.13-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /workspace

ARG VERSION=dev

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o manager cmd/main.go

# Runtime stage - distroless, non-root, no shell
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532
ENTRYPOINT ["/manager"]
