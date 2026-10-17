# User model policy override — effective immediately

The user explicitly overrides the older AGENTS.md delegation model rule. Astra owns architecture, risk, and final review. Use gpt-6.1-sol (or newer Sol) at high reasoning for implementation, investigation, and testing. Use gpt-6-astra (or newer Astra) for difficult architecture/risk/review. gpt-6-luna (or newer Luna) is permitted only for quick simple searches. Never spawn or resume gpt-5.6 models or gpt-6-sol. Native workers cannot change models through followup_task; retire old workers after checkpoint and use new explicit-model workers with fork_turns none. No recursive delegation.

All three old workers were instructed to stop reasoning/implementation, checkpoint existing artifacts/status only, then idle. No benchmark is active: tiny smoke1 closed naturally with status1; H2 withheld125. Completed diagnostics and frozen artifacts remain valid evidence and must not be restarted/discarded because of author model. This is continuation of the current WAN task, not a new review round.

Only worktree /home/user/.codex/worktrees/astra-proxy-performance/p2pstream. Draft PR329 to dev, current pushed00814fb. User root untouched. No merges/releases/deployment/liveconfig/devservers. Heavy workloads sequential. Fixedgates and allfailedattempts preserved.

## Enforcement completed

Retired/interrupted old native workers current_wan_prepare, wan_protocol_derivative, wan_evidence_package after durable handoff. Native list_agents confirms only Astra root and three allowed gpt-6.1-sol/high replacements active: wan_implementation_61 (all runner fixes/integration/contracts/launch + sole heavy execution), wan_review_61 (read-only independent review), wan_evidence_61 (package planning/export only). Each spawned with explicit model and fork_turns none; each instructed no recursive delegation. Old artifacts and failed tiny retained; no workload restarted because of model change.

Old handoffs: handoff/current-wan-prepare-model-policy-handoff-1/HANDOFF.md; review/wan-protocol-derivative-handoff-20261017.md; runner-review/RUNNER-SUCCESSOR-HANDOFF-20261017.md.
