# syntax=docker/dockerfile:1.7

# ─── Build stage ────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a static pure-Go binary (modernc SQLite needs no CGO).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o goapi .

# ─── Runtime stage ──────────────────────────────────────────────────────────
FROM alpine:3.22

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 gemini \
    && adduser -u 10001 -D -G gemini -h /app -s /sbin/nologin gemini \
    && mkdir -p /app/data /app/cookies /app/output \
    && chown -R 10001:10001 /app \
    && chmod 700 /app/data /app/cookies /app/output

COPY --from=builder /app/goapi /app/goapi
RUN chown 10001:10001 /app/goapi && chmod 0755 /app/goapi

USER 10001:10001

ENV TZ=UTC

EXPOSE 8100
EXPOSE 9226

HEALTHCHECK --interval=30s --timeout=3s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8100/health >/dev/null 2>&1 || exit 1

CMD ["./goapi"]
