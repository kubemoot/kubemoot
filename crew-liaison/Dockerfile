FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS builder

WORKDIR /build

# Operator module (local dependency for the Crew types)
COPY operator/api/ /operator/api/
COPY operator/pkg/ /operator/pkg/
COPY operator/go.mod operator/go.sum /operator/

COPY crew-liaison/go.mod crew-liaison/go.sum* /build/
RUN go mod download 2>/dev/null || true
COPY crew-liaison/ /build/

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X github.com/kubemoot/kubemoot/crew-liaison/internal/liaison.Version=${VERSION}" -o crew-liaison .

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /build/crew-liaison /crew-liaison
EXPOSE 8080
ENTRYPOINT ["/crew-liaison"]
