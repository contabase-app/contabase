FROM golang:1.26-bookworm AS builder

WORKDIR /app

ARG TAILWIND_VERSION=v4.3.0
RUN ARCH=$(uname -m) && \
    if [ "$ARCH" = "x86_64" ]; then TAILWIND_ARCH="x64"; \
    elif [ "$ARCH" = "aarch64" ]; then TAILWIND_ARCH="arm64"; \
    else echo "Unsupported architecture: $ARCH"; exit 1; fi && \
    curl -fsSL "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-${TAILWIND_ARCH}" -o /usr/local/bin/tailwindcss && \
    chmod +x /usr/local/bin/tailwindcss

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN tailwindcss -i assets/css/input.css -o assets/css/style.css --minify

ARG VERSION

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    APP_VERSION="${VERSION:-$(cat VERSION 2>/dev/null || echo dev)}" && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X 'github.com/contabase-app/contabase/internal/version.Version=${APP_VERSION}'" -o /app/server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/admin ./cmd/admin

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata mailcap

ENV TZ=America/Sao_Paulo
WORKDIR /app

COPY --from=builder /app/server .
COPY --from=builder /app/admin .
COPY --from=builder /app/templates ./templates
COPY --from=builder /app/assets ./assets
COPY --from=builder /app/VERSION /app/VERSION

RUN addgroup -g 1000 appgroup && \
    adduser -u 1000 -G appgroup -S -D appuser

RUN mkdir -p /app/data/uploads/profile /app/data/uploads/workspaces /app/data/backups && \
    chown -R appuser:appgroup /app/data

USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["./server"]
