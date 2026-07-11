ARG GO_VERSION=1.25.8

FROM golang:${GO_VERSION}-alpine AS builder

WORKDIR /src
RUN apk add --no-cache ca-certificates git make tzdata

COPY go.mod go.sum ./
RUN GOPROXY=direct go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w -X main.Name=aisphere-hub -X main.Version=${VERSION}" \
      -o /out/aisphere-hub \
      ./cmd/aisphere-hub

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S app \
    && adduser -S -G app app

WORKDIR /app

COPY --from=builder /out/aisphere-hub /app/aisphere-hub
COPY --from=builder /src/migrations /app/migrations
COPY --from=builder /src/configs /app/configs

RUN chown -R app:app /app

USER app

EXPOSE 18001 19001 19090

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://127.0.0.1:18001/healthz || exit 1

ENTRYPOINT ["/app/aisphere-hub"]
CMD ["-conf", "/app/configs"]
