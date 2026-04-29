FROM oven/bun:1.2.18 AS frontend
WORKDIR /app/web/management
COPY web/management/package.json web/management/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/management/ ./
RUN bun run build

FROM golang:1.25.6-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /out/p2pstream main.go

FROM golang:1.25.6-bookworm AS test-base
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        git \
        make \
        sqlite3 \
        unzip \
    && rm -rf /var/lib/apt/lists/*
ENV BUN_INSTALL=/usr/local/bun
ENV PATH=/usr/local/bun/bin:$PATH
WORKDIR /src
RUN curl -fsSL https://bun.sh/install | bash -s "bun-v1.2.18"
COPY go.mod go.sum ./
RUN go mod download
COPY web/management/package.json web/management/bun.lock ./web/management/
RUN cd web/management && bun install --frozen-lockfile
COPY . .

FROM test-base AS test
RUN make generate
RUN go test ./...
RUN cd web/management && bun run typecheck
RUN make build

FROM test-base AS race-test
RUN make generate
RUN go test -race ./...

FROM test-base AS smoke

FROM debian:bookworm-slim AS runtime
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=backend /out/p2pstream /app/p2pstream
COPY --from=frontend /app/web/management/dist /app/web/management/dist

ENV MANAGEMENT_UI_DIST_DIR=/app/web/management/dist
ENV MANAGEMENT_PORT=8081
ENV PORT=80

EXPOSE 80 443 8081
CMD ["/app/p2pstream", "server"]
