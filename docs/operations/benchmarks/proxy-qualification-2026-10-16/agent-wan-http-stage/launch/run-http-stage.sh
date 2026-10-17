#!/usr/bin/env bash
set -euo pipefail
unshare --user --map-root-user --fork env TMPDIR='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-wrapper-validation.45WH3a4h/tmp' GOTMPDIR='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-wrapper-validation.45WH3a4h/gotmp' GOCACHE='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-wrapper-validation.45WH3a4h/gocache' GOMODCACHE='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-wrapper-validation.45WH3a4h/gomodcache' GOPATH='/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-wrapper-validation.45WH3a4h/gopath' \
 '/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-source/scripts/qualify-proxy-agent-http-stage-stability.sh' \
 --runner '/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-source/scripts/qualify-proxy-agent-http-stage.sh' \
 --server-binary '/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-build.jISvFyke/final-bundle/bin/http-stage-server.test' \
 --agent-binary '/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-build.jISvFyke/final-bundle/bin/http-stage-agent.test' \
 --source-ref 364e7744603841f5d306f4c242f3acd2b04183bf \
 --build-manifest '/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/http-stage-build.jISvFyke/final-bundle/build-manifest.json' \
 --expected-runner-sha256 6188584e29e901f23723b1e3152009d2176999c858acde3c4c3cb9b2d0e5d0bb \
 --expected-server-sha256 3e74cf33e4814eaea0dd255f100fa36f1156b4672ae79bf990566002838f6a0d \
 --expected-agent-sha256 07bbdf9f85216d7d8100c3483cdfca2d72cd3a99de7c030a8a50574d24c1b31f \
 --expected-build-manifest-sha256 54a41841370ed5db8fad65cec2bddd1d97bca8e422fbd5e3d7ab111f784b249c \
 --output-dir '/home/user/.codex/worktrees/astra-proxy-performance/p2pstream/tmp/continuation-20261016/http-stage-screen'
