# November 2026 dependency update baseline

This update consolidates 15 pending dependency-version PRs into a maintenance PR
against `dev`. These are version updates, not 15 reported vulnerabilities.
The existing Dependabot PRs remain open for maintainer disposition.

| Dependabot PR | Effective version / disposition |
| --- | --- |
| #360 | `@vitejs/plugin-vue` 6.0.10 |
| #359 | `@bufbuild/protobuf` 2.16.0 |
| #358 | `@playwright/test` 1.64.1 |
| #357 | `@connectrpc/connect` 2.2.0; matching `connect-web` 2.2.0 |
| #356 | Vue 3.5.44 |
| #354 | `golang.org/x/mod` 0.42.0 |
| #353 | `github.com/andybalholm/brotli` 1.2.6 |
| #352 | `golang.org/x/net` 0.61.0 |
| #351 | `github.com/klauspost/compress` 1.20.3 |
| #350 | Bun 1.4.4 in both package managers, workflows and Docker |
| #349 | Docker CLI 29.9.2 in updater and test images |
| #348 | Go 1.27.3 in `go.mod` and all Go Docker stages |
| #223 | `github.com/mattn/go-sqlite3` 1.14.52 |
| #222 | `vue-router` 5.3.1 |
| #198 | Already satisfied: indirect gRPC 1.83.2 on base `576de01230e32f08eccc0072e7f8ea8bb4940490`; unchanged |

Bun regenerates the management lockfile. TypeScript remains at 6.0.3 and the
Dependabot 7.0 exclusion remains: `vue-tsc` requires the programmatic API.
Connect Web's exact Connect peer requires updating the companion to 2.2.0.
Protobuf generators remain unchanged; regeneration must produce no diff.
The additional Go x/crypto, x/sys, x/term, x/sync and x/text changes follow
x/net's module requirements rather than an unrelated upgrade sweep.

## Go HTTP header limits

[Go 1.27](https://go.dev/doc/go1.27) adds
[`Server.MaxHeaderValueCount`](https://pkg.go.dev/net/http@go1.27.3#Server),
whose default is 500 values, independent of the byte limit. The server retains
this default. The HTTP/2 decoded-header lease fixture now sends 400 repeated
values plus a 20 KiB value, staying below both parser limits while preserving
96 concurrent request owners and at least 96 KiB charged per request. A separate
regression checks that excessive small repeated values receive HTTP/2 431 before
handler admission and release all connection resources. The byte-limit and
cancellation/lease assertions remain in place.

## Docker and Compose

Docker bases retain multiarch manifest digest pins verified from Docker Hub's
registry API. The Go tag's current index differs from the older index in #348;
use the verified current index, not an unverified copied digest.

[Docker 29 release notes](https://docs.docker.com/engine/release-notes/29/) and
[API version history](https://docs.docker.com/reference/api/engine/)
are the upstream compatibility references. Updater operations invoke Docker CLI
and Compose rather than hardcoding an Engine API version. The requested CLI
image bundles Compose 5.6.1. The isolated updater rehearsal moves its disposable
engine to pinned Docker 29.9.2 too, retaining the independently pinned Compose
2.39.1 operator fixture so managed and native Compose execution exercise different
versions. Historical Docker 28 resource-normalization fixtures remain useful
coverage and retain their provenance comments. A runtime image build alone does
not qualify updater install/rollback/retry or native Compose behavior; the
canonical `make docker-server-updater-test` rehearsal is required for that claim.
