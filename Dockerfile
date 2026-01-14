FROM oven/bun:1 AS frontend-builder

WORKDIR /app/frontend

COPY frontend/package.json frontend/bun.lock ./
RUN bun install --frozen-lockfile

COPY frontend .
RUN bun run build

FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Copy built frontend assets
COPY --from=frontend-builder /app/internal/web/static/app ./internal/web/static/app

RUN CGO_ENABLED=0 go build -o bench ./cmd/bench

FROM rust:alpine AS inferno-builder

RUN apk add --no-cache musl-dev
RUN cargo install inferno --root /usr/local --locked

FROM alpine:latest

RUN apk add --no-cache ca-certificates graphviz ttf-freefont go

WORKDIR /app

COPY --from=builder /app/bench .
COPY --from=inferno-builder /usr/local/bin/inferno-flamegraph /usr/local/bin/

RUN mkdir -p /data/svg-cache

ENV SVG_CACHE_DIR=/data/svg-cache
ENV SVG_CACHE_MAX_RUNS=5

EXPOSE 8080

CMD ["./bench", "serve", "--db", "/data/bench.db", "--port", "8080"]
