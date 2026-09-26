FROM golang:1.26-alpine AS builder

WORKDIR /build

# Operator module (local dependency for the Crew types)
COPY operator/api/ /operator/api/
COPY operator/pkg/ /operator/pkg/
COPY operator/go.mod operator/go.sum /operator/

COPY crew-liaison/go.mod crew-liaison/go.sum* /build/
RUN go mod download 2>/dev/null || true
COPY crew-liaison/ /build/

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X github.com/javajon/kubemoot/crew-liaison/internal/liaison.Version=${VERSION}" -o crew-liaison .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /build/crew-liaison /crew-liaison
EXPOSE 8080
ENTRYPOINT ["/crew-liaison"]
