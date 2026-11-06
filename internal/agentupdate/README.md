# Agent-update release metadata

Managed updates use GitHub Releases as their authenticated distribution source.
The repository is pinned as `owner/repo`; arbitrary URLs, redirecting to another
host, mutable channel tags, and cross-channel releases are not accepted.

Every published stable or staging release contains
`p2pstream_agent_update_manifest.json`. The canonical JSON records:

- exact release channel, SemVer, commit, monotonic sequence, publication time,
  and expiry;
- minimum-safe version and security epoch rollback floors;
- management server, tunnel protocol, and rescue-updater compatibility ranges;
- exact filename, byte size, and SHA-256 for every agent binary and release
  attachment;
- the content-addressed OCI index and its per-platform descriptors.

The manifest has no separate signature or release-root metadata. Authentication
comes from the pinned GitHub repository and HTTPS/API boundary. The manifest's
digests still bind every downloaded byte to the release selected by management.

## Release workflow

`.github/workflows/release.yml` is a single build, verify, and publish pipeline.
A push to `staging` automatically publishes a unique
`vX.Y.Z-staging.N` GitHub prerelease and Linux `amd64`/`arm64` images. A stable
release is dispatched from `main` with an exact `vX.Y.Z` tag. No signing phase,
offline workstation, signing key, or per-release secret is required.

Configure these repository variables for both channels:

- `AGENT_UPDATE_MANIFEST_VALIDITY_DAYS`
- `AGENT_UPDATE_SECURITY_EPOCH`
- `AGENT_UPDATE_MIN_SAFE_VERSION`
- `AGENT_UPDATE_SERVER_MIN_VERSION` / `AGENT_UPDATE_SERVER_MAX_VERSION`
- `AGENT_UPDATE_UPDATER_MIN_VERSION` / `AGENT_UPDATE_UPDATER_MAX_VERSION`
- `AGENT_UPDATE_PROTOCOL_MIN` / `AGENT_UPDATE_PROTOCOL_MAX`
- `AGENT_UPDATE_PROTOCOL_CURRENT`

The pipeline validates the complete repository, builds both architectures and
their Docker images, constructs the OCI index, creates the canonical manifest,
re-verifies every artifact against it, publishes immutable version/commit
aliases, creates the GitHub release, reads every published asset back, and only
then moves the matching `staging` or `latest` image alias.

For local inspection, run `agentupdate-manifest verify-release` with the exact
expected version, channel, commit, sequence, server/protocol versions, and one
argument for every artifact, OCI index, and release attachment in the
manifest. Verification requires the inventories to match exactly; partial or
additional sets are rejected. The workflow contains the canonical invocation.

## Host-side authorization is separate

The Ed25519 management authority and per-host activator keys are intentionally
not release-signing keys. They authorize and attest privileged activate or
rollback actions on an enrolled machine. Removing artifact signing does not
allow an agent bearer token, compromised tunnel process, or unprivileged
updater worker to command the root-owned slot switch.
