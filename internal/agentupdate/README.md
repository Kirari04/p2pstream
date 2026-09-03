# Agent update trust metadata

The updater accepts only canonical, threshold-signed metadata for its locally
pinned `stable` or `staging` channel. The manifest contains channel and release identity, compatibility floors,
raw-binary hashes, and the exact OCI index digest plus its normalized child
descriptor set. It also contains an exact name/size/SHA-256 inventory of every
published non-signature release attachment, but never a URL. The updater must construct downloads
from its compiled repository origin, the verified `Version`, and the verified
artifact `Name`.

`RootMetadata` is an out-of-band trust anchor. Publishing
`p2pstream_agent_update_root.json` beside a release is useful for audit, but does
not make it trusted: production binaries must pin an approved root (or activate
a new root with `VerifyRootRotation`). Online rotation requires a canonical next
root signed by the current root threshold, exactly increments the root version,
and extends its expiry. An expired root requires out-of-band recovery. Root
version, release sequence, security epoch, and minimum-safe-version floors must
be stored durably after activation and supplied on every later verification.

The release workflow deliberately never receives a signing private key. Stable and staging
publishing is a two-phase operation:

1. Dispatch `Release channels` with `phase=prepare`, the matching channel, and an exact unused `vX.Y.Z` or `vX.Y.Z-staging.N`. A push to `staging` performs this prepare step automatically with a unique staging prerelease. CI
   builds the binaries and a content-addressed candidate image, then creates a
   draft containing the canonical unsigned manifest. No version, commit, or
   `latest` image alias exists at this stage.
2. Independent signers download that draft, verify its source commit and raw
   artifact hashes, then run a previously audited and pinned
   `agentupdate-manifest sign-partial` binary with one separately controlled key
   each. Never execute signing code from the candidate commit while a key is
   present. A trusted offline workstation combines the contributions with
   `agentupdate-manifest merge` and may run
   `agentupdate-manifest verify-release` for a final local check.
3. Base64-encode only the merged public signature envelope into the protected
   `agent-update-publish` environment secret
   `AGENT_UPDATE_OFFLINE_SIGNATURES_BASE64`, approve that environment, and
   dispatch the same version with `phase=publish`. CI re-downloads the unchanged
   draft, verifies the configured root, threshold, exact release identity,
   every release attachment, and the OCI index re-read by its signed registry
   digest before publishing it. Only then does it create normal image aliases. No
   draft asset or versioned release asset is replaced. Stable moves `latest`;
   staging marks a GitHub prerelease and moves only the `staging` image alias.

Enable GitHub immutable releases for the repository. The workflow verifies
every signed byte and never replaces versioned assets; the repository setting
adds a server-side immutability boundary after the release is published.

The workflow requires these GitHub configuration values:

- Protected-environment secret `AGENT_UPDATE_OFFLINE_SIGNATURES_BASE64`: the
  canonical threshold signature envelope for this one prepared manifest. It
  contains no private material and cannot authorize any other manifest.
- Variable `AGENT_UPDATE_ROOT_METADATA_BASE64`: canonical `RootMetadata` JSON,
  base64-encoded. Its public key set may be larger than the signing-key subset;
  the subset still has to meet its threshold.
- Variable `AGENT_UPDATE_MANIFEST_VALIDITY_DAYS`: manifest lifetime from 1 to
  3650 days. Because stable assets are immutable, operations must publish a new
  release before the current manifest expires.
- Variables `AGENT_UPDATE_SECURITY_EPOCH`, `AGENT_UPDATE_MIN_SAFE_VERSION`,
  `AGENT_UPDATE_SERVER_MIN_VERSION`, `AGENT_UPDATE_SERVER_MAX_VERSION`,
  `AGENT_UPDATE_UPDATER_MIN_VERSION`, `AGENT_UPDATE_UPDATER_MAX_VERSION`,
  `AGENT_UPDATE_PROTOCOL_MIN`, `AGENT_UPDATE_PROTOCOL_MAX`, and the current
  implementation value `AGENT_UPDATE_PROTOCOL_CURRENT`.

Example offline ceremony after the prepare phase (repeat the signing block on
separate signer systems, each with one key only). Enter the key through a
hidden prompt and standard input; never put it in a command line, environment
variable, shell-history entry, or candidate-controlled file:

```bash
gh release download v1.2.3 --dir candidate

read -r -s -p 'Offline Ed25519 key: ' P2PSTREAM_OFFLINE_KEY; printf '\n'
printf '%s\n' "$P2PSTREAM_OFFLINE_KEY" | \
  /opt/trusted-agentupdate-tools/agentupdate-manifest sign-partial \
  --manifest candidate/p2pstream_agent_update_manifest.json \
  --root candidate/p2pstream_agent_update_root.json \
  --signatures-output signer-a.json \
  --key-stdin
unset P2PSTREAM_OFFLINE_KEY

/opt/trusted-agentupdate-tools/agentupdate-manifest merge \
  --manifest candidate/p2pstream_agent_update_manifest.json \
  --root candidate/p2pstream_agent_update_root.json \
  --signature signer-a.json --signature signer-b.json \
  --output p2pstream_agent_update_manifest.signatures.json

/opt/trusted-agentupdate-tools/agentupdate-manifest verify-release \
  --manifest candidate/p2pstream_agent_update_manifest.json \
  --root candidate/p2pstream_agent_update_root.json \
  --signatures p2pstream_agent_update_manifest.signatures.json \
  --expected-version v1.2.3 \
  --expected-channel stable \
  --expected-commit '<exact-40-character-commit>' \
  --expected-sequence '<monotonic-sequence>' \
  --server-version v1.2.3 --protocol-version 1 \
  --artifact linux/amd64=candidate/p2pstream_v1.2.3_linux_amd64 \
  --artifact linux/arm64=candidate/p2pstream_v1.2.3_linux_arm64 \
  --oci-index ghcr.io/example/p2pstream=candidate/p2pstream_v1.2.3_image_index.json \
  --release-asset candidate/checksums.txt \
  --release-asset candidate/p2pstream_v1.2.3_linux_amd64 \
  --release-asset candidate/p2pstream_v1.2.3_linux_amd64.tar.gz \
  --release-asset candidate/p2pstream_v1.2.3_linux_arm64 \
  --release-asset candidate/p2pstream_v1.2.3_linux_arm64.tar.gz \
  --release-asset candidate/p2pstream_v1.2.3_image_index.json \
  --release-asset candidate/p2pstream_v1.2.3_image_linux_amd64.txt \
  --release-asset candidate/p2pstream_v1.2.3_image_linux_arm64.txt \
  --release-asset candidate/p2pstream_v1.2.3_source.tar.gz \
  --release-asset candidate/p2pstream_agent_update_root.json

base64 -w0 p2pstream_agent_update_manifest.signatures.json
```

Every signer must inspect the exact commit, raw artifact hashes, OCI index
digest, and exact platform child descriptors before signing. Resolve the
candidate index by digest and independently inspect or reproduce its contents.
Ordinary Docker tag pulls do not verify this update signature, so deployments
that need the same trust property must pin the signed digest or enforce an
equivalent OCI admission policy. Use a network-isolated host and verify the
pinned tool binary itself; the
cryptographic threshold is not a substitute for release review. Remove the
per-release signature-envelope secret after publication so an operator cannot
mistakenly reuse it for the next draft (the next manifest would reject it
cryptographically in any case).

Root configuration must remain byte-for-byte stable for a root version. Change
the root version whenever its key set, threshold, or expiry changes, and ship
the corresponding trust anchor before signing manifests against it. Never put
a threshold quorum—or even one production private key—into a repository,
GitHub secret, Actions runner, or the management server.
