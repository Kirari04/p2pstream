ARG VERSION=dev
ARG COMMIT=""
ARG SOURCE_REPOSITORY=Kirari04/p2pstream
ARG VITE_RELEASE_REPOSITORY=""
ARG VITE_RELEASE_REF=""

FROM oven/bun:1.2.18 AS frontend
WORKDIR /app/web/management
ARG VITE_RELEASE_REF
ARG VITE_RELEASE_REPOSITORY=""
ENV VITE_RELEASE_REPOSITORY=$VITE_RELEASE_REPOSITORY
ENV VITE_RELEASE_REF=$VITE_RELEASE_REF
COPY web/management/package.json web/management/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/management/ ./
RUN bun run build

FROM golang:1.25.10-bookworm AS backend
WORKDIR /src
ARG VERSION
ARG COMMIT
ARG SOURCE_REPOSITORY
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "-s -w -X p2pstream/internal/buildinfo.Version=${VERSION} -X p2pstream/internal/buildinfo.Commit=${COMMIT} -X p2pstream/internal/buildinfo.Repository=${SOURCE_REPOSITORY}" -o /out/p2pstream main.go

FROM scratch AS binary
COPY --from=backend /out/p2pstream /p2pstream

FROM golang:1.25.10-bookworm AS test-base
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

FROM golang:1.25.10-bookworm AS smoke-upstream-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/smoketest/upstream ./internal/smoketest/upstream
RUN CGO_ENABLED=0 go build -trimpath -o /out/smoke-upstream ./internal/smoketest/upstream

FROM scratch AS smoke-upstream
COPY --from=smoke-upstream-build /out/smoke-upstream /smoke-upstream
EXPOSE 9000
CMD ["/smoke-upstream"]

FROM test-base AS legal
ARG VERSION
ARG COMMIT
ARG SOURCE_REPOSITORY
RUN VERSION="${VERSION}" COMMIT="${COMMIT}" SOURCE_REPOSITORY="${SOURCE_REPOSITORY}" make legal-notices

FROM debian:bookworm-slim AS runtime
WORKDIR /app
ARG VERSION
ARG COMMIT
ARG SOURCE_REPOSITORY
LABEL org.opencontainers.image.licenses="AGPL-3.0-or-later" \
      org.opencontainers.image.source="https://github.com/${SOURCE_REPOSITORY}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}"
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates libcap2-bin \
    && rm -rf /var/lib/apt/lists/*
COPY --from=backend /out/p2pstream /app/p2pstream
COPY --from=frontend /app/web/management/dist /app/web/management/dist
COPY --from=legal /src/dist/legal /app/legal

RUN groupadd --system p2pstream \
    && useradd --system --gid p2pstream --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin p2pstream \
    && mkdir -p /data \
    && chown -R p2pstream:p2pstream /app /data \
    && setcap 'cap_net_bind_service=+ep' /app/p2pstream

USER p2pstream:p2pstream

ENV ENV=production
ENV MANAGEMENT_UI_DIST_DIR=/app/web/management/dist
ENV MANAGEMENT_PORT=8081
ENV CONFIG_DIR=/data

VOLUME /data
EXPOSE 80 443 8081
CMD ["/app/p2pstream", "server"]
