# Independent offline-validator artifact closure

The immutable offline-validator evidence is internally accepted. This is an artifact-only conclusion; the original outer tool-envelope statuses remain 1 and unresolved.

The review directly verified these frozen ledgers:

- source checkpoint: `6ab37ae85ca1d99e99d0387c1d3a7c99f4647a8517ff31686bfb6354343cbe9f`
- nine-case contract: `9c10da08db3cf37ccedd89972871a2844f22f19376edd14c651195b88950857b`
- H1/H2 offline verification: `64da5bd3f06325b2678e93d14f73a5ed8b972383006ad96826d5d62c77c8683a`
- closed smoke run: `83770a2a4e978d9be76954753c704c1ab2d511eef096aa8cfcc95646141ccae8`
- canonical smoke result review: `37c4e3d836f2b52542369d1e58526ce0ea86e895251fb90266bdbda77d3b9a31`

The source delta adds only the four preflight keys emitted by cp9: `base_cp7_checkpoint`, `base_cp7_reviewed_manifest`, `base_cp7_runner`, and `sender_harness_receipt`. The contract contains exactly nine expected outcomes: the original shape rejects, the corrected shape accepts, each missing new key rejects, an unsupported extra rejects, a malformed hash rejects, and changed pre/post manifests reject. All direct case outcomes match their exact expected status and issue text.

The frozen offline H1 and H2 attestations are accepted with no issues. Both retain 16 rows, runner status 0, natural endpoint and namespace exits, joined outer runner, server-only delegated cgroup, valid sender terminal closure, and four valid wall-contained sender intervals. Their result, lifecycle, and summary hashes bind the closed raw inputs. The original arm attestations remain unchanged and rejected solely for the incomplete preflight key set.

The raw pre/post manifests are byte-equal for both arms. They bind the published server binary `f2e6cf390fdcd727b8c7120a6d877dc3a53afe9d8b970aa7b0d7f6cc892a7bdc`, agent binary `43eadb7664d70f9585887ac824cc7e6dcb55d6fa40731e3888b6fa5a1cc8d74f`, and audited build manifest `ba1a31b8f54e7cbb4df8dace0990a4aba87fd4462803a31a6e3082810e9e1b2b`.

The independent artifact audit itself exits 0. It did not invoke either validator, any test, build, endpoint, or workload. Both original tool-envelope records remain status 1 despite direct/saved status 0 and complete ledgers. No supported internal failing condition is identified in this review, so those outer statuses are preserved as unresolved rather than relabeled successful.
