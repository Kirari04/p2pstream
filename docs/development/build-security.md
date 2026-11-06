# Build inputs, provenance and Docker smoke tests

## Pinned build inputs

Docker build stages use tagged, digest-pinned multi-platform images. The test
stage copies Bun from the pinned Bun image and supplies `bunx` as a symlink to
that executable. It does not execute the remote Bun installer. GitHub Actions
use full upstream commit pins with readable version comments. Dependabot opens
Docker and action updates against `dev`; review and test those updates normally.

Digest pins fix the base-image bytes. Network package installs and the hosted
runner remain additional build inputs; these pins alone do not make the entire
build hermetic or byte-for-byte reproducible.

## Release image provenance

The release workflow publishes signed SLSA build provenance using GitHub's
official `actions/attest` action. Each server architecture manifest
is a separate subject named by its image repository and exact Buildx output
digest. The final server index is also attested, after validating its bytes,
digest and exactly two executable Linux descriptors (`amd64` and `arm64`).

Attestations are stored in GitHub and pushed separately to the image registry.
They are not added as children of the executable image index. Inline BuildKit
`provenance: false` keeps the executable image index limited to its two
platform descriptors and per-architecture executable manifests. Attestation failure
fails the release job before release publication. Only the image/release jobs
receive `id-token: write` and `attestations: write`; signing uses short-lived
GitHub OIDC credentials and needs no stored signing key. Optional organization
artifact storage records are disabled.

To verify a newly attested stable image, select its exact digest and expected
source commit from the release you intend to trust, then run with a recent
authenticated GitHub CLI (replace the placeholders):

```bash
image=ghcr.io/kirari04/p2pstream
digest=sha256:REPLACE_WITH_IMAGE_DIGEST
commit=REPLACE_WITH_EXPECTED_SOURCE_COMMIT
gh attestation verify "oci://${image}@${digest}" \
  --repo Kirari04/p2pstream \
  --signer-workflow Kirari04/p2pstream/.github/workflows/release.yml \
  --source-digest "$commit" --source-ref refs/heads/main \
  --deny-self-hosted-runners
```

Use `refs/heads/staging` for staging releases. Verify the final index digest and
the architecture digest actually deployed. By default the CLI obtains attestations from GitHub; add
`--bundle-from-oci` to obtain them from the registry instead. Registry access
requires authentication as described by the CLI. Older releases predate these
attestations. See the upstream [action documentation](https://github.com/actions/attest)
and [CLI verification options](https://cli.github.com/manual/gh_attestation_verify).

The signature establishes the named workflow/source identity's claim about the
subject bytes. It is not an independent code review, a complete BuildKit
materials inventory, an SBOM or a guarantee of build isolation. Managed agent updaters verify their release identity and digest contracts;
they do **not** enforce these attestations. Verification is an operator check.

## Disposable Docker smoke tests

Run `make docker-smoke` (or `scripts/run-docker-smoke.sh`) from a source checkout.
The runner generates independent cryptographically random setup, agent and
admin credentials once per run and shares them with the appropriate containers.
There are no credential defaults. Direct Compose invocation fails without the
required runner variables. Both Make and Compose bypass the application's
`.env` for these smoke targets.

Ports `18080`, `18081`, `18088`, `18089` and `18443` publish only on `127.0.0.1`.
The server and agents exercise the runtime filesystem/capability restrictions,
including management TLS generation and public TLS on container port `443`.
The runner removes containers, networks and test volumes on exit, including
test failure. `make docker-smoke-clean` recovers an interrupted stack without
saved credentials. Its project is scoped to the checkout; run only one smoke
invocation per checkout at a time. No application data volume is reused.

For a smoke stack left by an older checkout, run `docker compose -f docker-compose.test.yml down -v` from that older checkout once, after confirming its project contains only disposable smoke data. The current clean target does not remove stacks or volumes under the old directory-derived project name.
