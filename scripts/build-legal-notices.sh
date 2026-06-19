#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LEGAL_DIR="${ROOT_DIR}/dist/legal"
VERSION_VALUE="${VERSION:-dev}"
COMMIT_VALUE="${COMMIT:-}"
SOURCE_REPOSITORY_VALUE="${SOURCE_REPOSITORY:-Kirari04/p2pstream}"

repository_slug="${SOURCE_REPOSITORY_VALUE%.git}"
repository_slug="${repository_slug#https://github.com/}"
repository_slug="${repository_slug#http://github.com/}"
repository_slug="${repository_slug#git@github.com:}"
repository_slug="${repository_slug#/}"
repository_slug="${repository_slug%/}"
if [[ -z "${repository_slug}" ]]; then
  repository_slug="Kirari04/p2pstream"
fi

source_ref="${VERSION_VALUE}"
if [[ -z "${source_ref}" || "${source_ref}" == "dev" || "${source_ref}" == "nightly" || "${source_ref}" == "staging" ]]; then
  source_ref="${COMMIT_VALUE}"
fi

source_url="https://github.com/${repository_slug}"
if [[ -n "${source_ref}" ]]; then
  source_url="${source_url}/tree/${source_ref}"
fi

rm -rf "${LEGAL_DIR}"
mkdir -p "${LEGAL_DIR}/third-party"

cp "${ROOT_DIR}/LICENSE" "${LEGAL_DIR}/LICENSE"
cp "${ROOT_DIR}/NOTICE" "${LEGAL_DIR}/NOTICE"

cat >"${LEGAL_DIR}/SOURCE.txt" <<EOF_SOURCE
p2pstream Corresponding Source

License: AGPL-3.0-or-later
Copyright: Copyright (C) 2026 p2pstream contributors
Repository: https://github.com/${repository_slug}
Source reference: ${source_ref:-repository default branch}
Corresponding source: ${source_url}

This directory contains license notices for the p2pstream distribution. The
preferred form for modifying p2pstream is the source tree at the corresponding
source URL above, including go.mod, go.sum, bun.lock files, Dockerfile,
Makefile, scripts, generated-code inputs, and documentation.
EOF_SOURCE

cd "${ROOT_DIR}"
go run github.com/google/go-licenses@v1.6.0 save . --ignore p2pstream --save_path "${LEGAL_DIR}/third-party/go" --force
bun scripts/collect-node-licenses.mjs --output "${LEGAL_DIR}/third-party/web-management"
