#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LEGAL_DIR="${ROOT_DIR}/dist/legal"

rm -rf "${LEGAL_DIR}"
mkdir -p "${LEGAL_DIR}/third-party"

cp "${ROOT_DIR}/LICENSE" "${LEGAL_DIR}/LICENSE"
cp "${ROOT_DIR}/NOTICE" "${LEGAL_DIR}/NOTICE"

cd "${ROOT_DIR}"
go run github.com/google/go-licenses@v1.6.0 save . --ignore p2pstream --save_path "${LEGAL_DIR}/third-party/go" --force
bun scripts/collect-node-licenses.mjs --output "${LEGAL_DIR}/third-party/web-management"
