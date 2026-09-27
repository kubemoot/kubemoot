FROM golang:1.27-alpine AS builder

WORKDIR /build
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o scheduling-mcp ./cmd/scheduling-mcp/

FROM scratch
COPY --from=builder /build/scheduling-mcp /scheduling-mcp
ENTRYPOINT ["/scheduling-mcp"]
