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

FROM debian:bookworm-slim AS runtime
WORKDIR /app
COPY --from=backend /out/p2pstream /app/p2pstream
COPY --from=frontend /app/web/management/dist /app/web/management/dist

ENV MANAGEMENT_UI_DIST_DIR=/app/web/management/dist
ENV MANAGEMENT_PORT=8081
ENV PORT=80

EXPOSE 80 8081
CMD ["/app/p2pstream", "server"]
