FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build

# Copy operator module (local dependency)
COPY operator/api/ /operator/api/
COPY operator/go.mod operator/go.sum /operator/

# Copy gateway module
COPY discussion-gateway/go.mod discussion-gateway/go.sum* /build/
RUN go mod download 2>/dev/null || true
COPY discussion-gateway/ /build/

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o discussion-gateway .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=builder /build/discussion-gateway /discussion-gateway
ENTRYPOINT ["/discussion-gateway"]
