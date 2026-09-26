FROM golang:1.26-alpine AS builder

WORKDIR /build

# Copy operator module (local dependency)
COPY operator/api/ /operator/api/
COPY operator/go.mod operator/go.sum /operator/

# Copy gateway module
COPY discussion-gateway/go.mod discussion-gateway/go.sum* /build/
RUN go mod download 2>/dev/null || true
COPY discussion-gateway/ /build/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o discussion-gateway .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /build/discussion-gateway /discussion-gateway
ENTRYPOINT ["/discussion-gateway"]
