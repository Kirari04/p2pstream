# Original96 qualification source review

Decision: **GO for the single root-owned offline boundary command.** No remaining harness/build-binding source blocker was found. Traffic still requires its separate final source/grant binding and the offline result. This reviewer performed source inspection and file/hash comparisons only; no validator, test, endpoint, namespace or traffic was executed.

The copied original owned runner differs only by two finite read-only transport-default snapshots: before the origin starts and after the final cohort. Removing those two lines reconstructs the original file exactly. Its timed `run_transfer` is independently byte-identical: 6,243 bytes, SHA256 `e4ad3f67ecd868cd3532ee917441cf4a79c96c3c786ffb92cbe5e6f8995ae567`. No additional between-request poll, warmup, retry, traffic, deadline tuning or timing-path change was introduced.

The original one-origin/routed-topology lifetime spans all three fresh endpoint pairs, ordered L/A, A/L, L/A. Each cohort still performs four 256 MiB download and four upload warmups with pool retirement after each, both retained transfers and the original steady D/U/U/D/D/U sequence. Exactly 96 transfers remain required. The aggregate gate still requires all four receiving pools at both endpoints to exceed 8 MiB and be quiescent, and per-row evidence remains unchanged. Each pair must pass download and upload median ratios of at least 0.90, and the original runner's pooled direction gates remain authoritative. A failing original runner status cannot be overridden by passing pair gates. No failed pair can be overridden by pooled success.

The same freshly built ordinary server is used in both modes. The fresh ordinary aggregate agent and the separately proven exact historical legacy executable are bound by hash and source evidence; no historical measurement serves as a control. The current reference identifies the actual patched 430-file source, not the old 8ac revision or an unqualified clean HEAD. The validator changes only its source-identity comparisons to the reviewed manifest. Build receipt `d428e45fd861c8ed7a4346920e53c2a56d1fe678660fd343707f9841f53af624` was independently reviewed separately.

The wrapper checks the verifier's exit status explicitly before reading its five tuple lines; the verifier emits those lines only after all checks succeed. It revalidates the same inputs after execution. The frozen closure contains all 430 compiled source inputs with their exact focused-check hashes, both current binaries, the legacy binary, original runner/provenance inputs and the transitive focused/build ownership evidence. The fresh runtime result path rejects retries or overwrites. The standalone supervisor retains the original inner ownership path, uses a 13,320-second outer bound around the unchanged 13,200-second wrapper, preserves all logs and rejects forced cleanup, adoption, remnants and observed artifact overflow. The inherited fixture deadline remains 12,591 seconds and transfer timeout 188 seconds. The standard workload has no newly imposed process-memory cgroup; the 256 MiB qualification-artifact threshold is a polled rejection bound, not a runtime RSS claim.

The read-only default helper checks CUBIC and integer idle1 without setting either sysctl. Host samples join the stable outer wrapper parent and outer parent namespace. The canonical runner creates a second network namespace inside the outer isolation boundary; its samples therefore join the lifecycle's actual runner namespace, while PID namespace/PID1 establish the surrounding ownership relationship. Agent-origin/router samples join exact helper PID/start/network identity and natural helper exits. Pre/post ordering and boot identity are checked. These are point-in-time namespace-default observations, not per-socket controller capture or a continuous scope guarantee. Existing lifecycle records do not establish per-endpoint host PID/start mappings, so the review makes no such claim.

The five authored offline cases exercise nested default scope and malformed/mismatched evidence, exact current/legacy tuple rejection, nonzero verifier status despite partial five-line output, complete synthetic 96-row pair gates and original pooled-status rejection, and unchanged timed/cohort bodies. They reuse only the old fixture's schema shape, replacing its rates and provenance synthetically; they are not historical throughput controls. The focused 60-second inner/90-second outer command executes no Go, sockets, endpoints or namespace creation. Its read-only build verifier is explicitly within scope.

Reviewed freeze:

- Contract plan: `1c69369f8897d8ea7bd3771ff7d1e56a125de99e262ac06f35ac892f42f1db45` (514 pinned inputs).
- Contracts: `ab461e36089c65299d74c216499152e7eeebb743071170061eac03dcc0eb55bf`.
- Driver: `580019e1b6199afec68e1532140615dce5d3813ac7b831dedf7e8dc3f8237dca`.
- Runtime guard: `48be97fc46c90d5f87d6de19df1b39e72ae3750be77d8299d7890eeb6b6fc070`.
- Default helper: `69b0735ad234c51c5362b7f3043427b26fa43c4e2bbe9d7e52cdd0df6067652d`.
- Manifest: `624b8979038417e25fd64bbdc3ea8d8d7db8c0de5f28ce349b28d5192d7b72b2`.
- Owned runner: `eac4b98a50df86cc71c026da8f0a7ab27a60543bf5eac9054ce6be410a913d8e`.
- Wrapper: `48043824b36dc6b64b18a4406d6be62da9753fdfed414feb62b9adb7d017c023`.
- Validator: `2384b55b34789d1956e2ccb227dd31af98f10919ce79ee73e6b908580bce937c`.

This qualification can establish the original current-versus-legacy throughput acceptance outcome. It cannot uniquely attribute TCP behavior, establish a randomized treatment effect, validate pressure/cancellation workloads, substitute for HAProxy comparisons or authorize a release. The earlier incomplete early-share run and failed original evidence remain unchanged.
