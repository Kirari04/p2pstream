# Sites implementation plan

Status: core implementation completed on `feat/sites-workspace`, with local backend, migration, frontend, and browser validation passing. Traffic-policy scope and the optional routing tester remain deferred. Final review and CI are tracked by the pull request to `dev`.

## Delivered behavior and verification

- Sites own shared routes across editable listener bindings, support named and Default modes, and start as drafts until Publish. Disabled published Sites retain claims; detaching the last listener preserves inactive configuration.
- The Site workspace includes nested route/target, listener, and TLS editing, compact actionable readiness, explicit independent saves, and desktop/mobile keyboard behavior.
- Schema 20 preserves existing Sites and route identities. The explicit standalone migration previews fallback copies and semantic exceptions, rejects stale revisions, and commits ownership, references, and runtime activation atomically. Unmigrated listeners retain compatibility; new and migrated listeners reject standalone authoring.
- Verified locally: full Go suite (including integration and DB migration tests), Go vet, focused race tests, 267 frontend tests, typecheck, production build, two bounded Site/migration browser scenarios, documentation build, deterministic generation, installer lifecycle checks, Yamux tests, and the isolated TCP WAN throughput regression. Docker fixture compilation and updater-script syntax pass; the full Docker rehearsal remains a CI check.
- Upgrade rollback requires restoring the pre-upgrade database backup; schema 20 deliberately rejects an unsafe down migration. No release promotion is included in this feature PR.

## Implementation order and completion criteria

1. **Model and runtime:** define Site ownership, listener bindings, draft/publication state, hostname selection, Default Sites, and serve/redirect behavior. Implement schema/API changes and request-context handling with focused invariant tests. Keep existing configurations functioning while the migration path is built.
2. **Migration:** implement and verify a revision-bound preview, conversion, identity/reference mapping, and recovery strategy against representative existing configurations. Resolve wildcard, priority, and shared-fallback cases before retiring standalone routing.
3. **Site workspace:** build Site configuration with nested route/target editing, editable listener assignments, and draft-to-Publish flow. Keep unfinished parent edits and keyboard context intact across child editors.
4. **Readiness and cutover:** integrate actionable readiness, complete browser/keyboard/narrow-screen verification, update documentation, and validate migration end to end. Remove standalone authoring/API behavior only when the new workflow and a migration outcome for existing configurations are verified.
5. **Deferred policies:** implement Global/Selected Sites traffic-policy scope only after stages 1–4 are complete and verified. The optional routing tester is not a prerequisite or part of the agreed initial implementation.

Initial delivery is complete when a user can create a draft Site, assign multiple listeners, configure routes and targets entirely inside it, fix readiness issues in context, and publish; then edit or move that Site without recreating routes. Tests must also establish hostname isolation, unchanged listener exposure during migration, preserved routing outcomes or explicit migration exceptions, and preservation of shared Sites when listeners are removed.

Use the repository's normal PR workflow: changes land in `dev` first; release promotion remains `dev → staging → main`. Do not start local development servers unless explicitly requested.

## Make Sites the route management workflow

Request flow: **Listener → Site → Route → Targets**. A Site owns its hostnames and routes and can be attached to multiple listeners. All configured listeners use that Site's shared route list; per-listener route overrides are outside the initial scope.

### Site ownership and editing

- [x] Create, edit, clone, order, and delete routes within their owning Site. Choose or create a Site before adding a route; remove standalone route creation and the Site-versus-Legacy scope selector once migration is ready.
- [x] Keep route capabilities available inside Sites: path matching, priorities, defaults, forwarding, redirects, static responses, target pools, access policies, and path security settings.
- [x] Manage domains and Serve/Redirect aliases on the Site. Defer adding multiple host patterns to standalone routes.
- [x] Allow specific-hostname Sites to serve a list of exact or wildcard hostnames without a mandatory primary hostname, including wildcard-only Sites. Make a canonical hostname optional and require an explicit exact canonical destination when configuring alias redirects; keep wildcard matching and certificate coverage semantics clear.
- [x] Remove the separate Routes management flow. If an aggregate route view is retained for search or diagnostics, make each entry lead to its owning Site.

### Polished Site workspace and nested route editing

The Site must be a complete workspace for its configuration. Required flow: **Open or create Site → see its routes → Add/Edit Route in a child modal or drawer → save or cancel → return to the same open Site**. Users must never need to navigate manually to a separate Routes tab to finish Site setup or manage its routes.

- [x] Keep the newly created draft Site open and immediately show its route section, with a clear empty state and `Add first route` action. Persist the draft Site before saving child routes; publication is not required to save routes. A failed draft creation preserves entered values and cannot create orphan routes. Existing Sites show their routes directly in the workspace.
- [x] Create new Sites as persisted drafts with no live hostname claims. Allow complete route and listener setup before an explicit `Publish` action atomically activates the Site and its configured behavior; revalidate conflicts and configuration at publication. Draft route saves remain inactive. Drafts are distinct from disabled published Sites, which retain hostname ownership.
- [x] Allow drafts to propose hostnames and Default Site bindings that are currently reserved; show conflicts in readiness and reject publication until resolved. Drafts never reserve hostname/default ownership or affect live traffic.
- [x] Provide a clear route list showing path match, action/target summary, priority, default status, and enabled state. Support adding, editing, cloning, ordering, enabling/disabling, and deleting routes from this list.
- [x] Open route configuration as a child modal or drawer above the Site workspace, preserving the Site underneath. Automatically bind new/cloned routes to that Site; do not ask users to choose the Site or listeners again.
- [x] Include full route and target configuration in this nested workflow. Keep target details inline or expandable where practical, avoiding an unnecessary third layer of dialogs.
- [x] After a successful route save, close only the route editor, refresh the Site's route list, and retain the same Site, section, filters, scroll position, and unsaved Site edits. Make the added or updated route easy to identify.
- [x] Canceling route edits returns to the unchanged Site workspace. Validation or save errors stay in the route editor with the entered values intact and clear feedback.
- [x] Keep Site-setting saves and route saves explicit and independent. Saving a route must not silently save or discard unrelated Site edits; protect unsaved edits when closing their editor.
- [x] When a published Site has unsaved hostname/listener edits, make the route editor's effective saved Site context explicit. A route save must not appear to apply unsaved parent settings; show which saved bindings it affects and preserve the parent draft.
- [x] Manage stacked-dialog focus and dismissal deliberately: only the top editor is interactive, keyboard focus is trapped there, Escape/backdrop actions affect only that editor, and closing it returns focus to its originating Site control. Apply unsaved-change handling before dismissal.
- [x] Refine spacing, typography, route-row hierarchy, action placement, empty/loading/error states, and responsive layouts consistently with the existing management UI. On narrow screens, preserve the same Site-to-route-to-Site flow with readable full-width editors.
- [x] Verify the complete interaction in the browser and end-to-end tests: create a Site, add/configure multiple routes and targets, edit/clone a route, save/cancel, and continue editing the Site without leaving its workspace or losing state. Include keyboard navigation, failed saves, and narrow-screen layouts.

### Site readiness summary

- [x] Show a compact readiness summary in the Site workspace during setup and after publication, with an overall status and expandable details. Keep draft/published/disabled lifecycle state distinct from configuration readiness.
- [x] Cover listener assignments and state, hostname ownership conflicts, certificate coverage per hostname and HTTPS listener, route availability, and incomplete route/target or redirect configuration. Identify the affected listener or route rather than hiding partial readiness behind one successful check.
- [x] Make every actionable item open its relevant settings or nested editor directly, preserving Site context. Examples include `Add listener`, `Add first route`, `Edit route`, and `Configure certificate`.
- [x] Distinguish blocking configuration errors, warnings, unknown checks, and checks that do not apply. Reuse authoritative validation so readiness agrees with Publish; allow incomplete drafts to be saved. Configuration readiness must not imply verified public DNS, network reachability, or healthy backends without supporting evidence.
- [x] Evaluate requirements according to Site and listener behavior: HTTP needs no certificate, Default Sites need no named primary hostname, valid redirect-only behavior does not require proxy targets, and intentionally absent default routes are not automatically errors.
- [x] Refresh the summary after relevant saves without resetting the Site workspace. Clearly distinguish persisted readiness from any preview of unsaved edits; show concise explanations and accessible status labels beyond color alone.

### Multiple and editable listener assignments

- [x] Replace the Site's single immutable listener with explicit Site-to-listener bindings. A Site can be served through multiple HTTP(S) listeners with one shared set of hostnames, routes, and targets.
- [x] Provide an editable `Listeners` selector when creating or editing a Site. Allow adding, removing, or replacing assignments without recreating the Site or cloning its routes.
- [x] Let each listener assignment select `Serve Site` or `Redirect to a selected HTTPS listener`, while retaining one shared Site route list. Configure and validate destination host/port, alias behavior, path/query preservation, and loop prevention; Default Sites require an explicit redirect destination. Retain ACME challenge handling before Site redirects.
- [x] Make routes depend on Site ownership rather than one stored listener assignment. Continue using the actual incoming listener for routing context, listener policies, authentication behavior, cache isolation, and observability.
- [x] Validate published hostname ownership and exact/wildcard precedence independently on each selected listener. Reject duplicate published ownership of the same normalized hostname pattern rather than transferring another Site's hostnames; allow reuse on separate listeners. An exact binding takes precedence over a matching wildcard binding, preserving that Site's ownership even when it is disabled.
- [x] Apply assignment changes atomically after validating all selected listeners. Preserve the Site ID, route IDs, targets, secrets, and policies when moving or adding bindings.
- [x] Show TLS/certificate coverage per hostname and listener, including HTTP as not applicable. Preserve authority/SNI checks on HTTPS and evaluate protocol-dependent access and redirect behavior on each binding.
- [x] Detaching or deleting a listener must remove only that listener's bindings, not delete shared Sites or their routes. Support an unassigned Site as visibly inactive configuration that can be attached again; removing its last binding stops its traffic without destroying configuration.
- [x] Explain that removing a binding releases the Site's hostname ownership on that listener, so requests may subsequently reach its Default Site or another eligible hostname binding. Do not confuse detaching with disabling a still-bound Site, which continues to reserve its hostnames.
- [x] Migrate each existing Site's listener into one binding without automatically merging similarly named Sites. Preserve previously listener-scoped names with unambiguous Site IDs and labels. Keep existing Sites in the published lifecycle, preserving enabled state and hostname ownership; never silently convert existing Sites into unclaimed drafts.
- [x] Update the API, database relationships and deletion behavior, runtime indexes, UI, and documentation for multiple editable bindings. Verify adding, moving, detaching, deleting a listener, conflicting assignments, and serving one Site across HTTP and HTTPS listeners.

### Default Site per listener

- [x] Add two hostname modes: `Specific hostnames` and `Any unmatched hostname`.
- [x] Allow at most one published Default Site binding per listener, including a disabled published Default Site. It is optional and requires no primary hostname or canonical domain; conflicting draft assignments remain inactive until publication resolves the conflict.
- [x] Allow the same Default Site to be assigned to multiple listeners while enforcing the one-Default-Site limit independently on every listener.
- [x] Select a matching named Site first; otherwise select the listener's Default Site. With no enabled Default Site, unmatched hosts receive 404.
- [x] Preserve exclusive hostname ownership: a disabled named Site or a missing path must never fall through to the Default Site or another Site.
- [x] Let the Default Site contain ordinary path routes and its own default route. Preserve the requested hostname; any canonical-host redirect must be explicitly configured.
- [x] Explain the distinction between a Default Site for unmatched hostnames and a default route for unmatched paths within one Site. Hostname matching applies to HTTP(S) routing on the selected listener; HTTPS still requires certificate coverage.

### Migration of existing standalone routes

- [x] Inventory and close remaining capability gaps before removing standalone routing, including existing host-pattern behavior and configurations without an exact primary domain.
- [x] Design a migration preview that maps existing routes into Sites, keeps already configured Site ownership intact, and identifies behavior changes or configurations requiring review.
- [x] Bind migration previews to the reviewed configuration revision; reject stale previews and regenerate them after configuration changes. Define the standalone API cutover, handling of unmigrated configurations, and recovery from failed or partially completed migration before rollout.
- [x] Introduce bindings/publication state without changing existing routing, migrate existing Sites to equivalent published bindings, then convert standalone routes through the validated preview. Keep the pre-conversion routing authoritative if a preview is rejected/stale or conversion fails. Retain compatibility storage for unmigrated listeners; new and successfully migrated listeners enforce Site ownership.
- [x] Preserve original listener exposure when migrating routes; do not attach migrated Sites to additional listeners automatically.
- [x] Migrate eligible host-specific groups into hostname Sites and catch-all behavior into the listener's Default Site only where request routing remains equivalent.
- [x] Account for overlapping exact/wildcard rules, cross-host route priorities, path misses that currently reach a listener-wide rule or default, and differing wildcard, authority-parsing, and HTTPS/SNI semantics. Grouping routes by hostname alone is not a sufficient migration algorithm.
- [x] Preserve route identity and references where possible, targets, secrets, access policies, enabled state, and default behavior. Make the migration transactional and safe to retry.
- [x] Create Sites resulting from an approved standalone conversion as published, not drafts. Commit route ownership/copies, bindings, Default Site assignments, publication, and the removal of standalone eligibility together; activate one complete validated runtime snapshot without intermediate partial routing. Preserve disabled-rule outcomes without introducing hostname reservations that change previously eligible fallback behavior.
- [x] Define how migration handles shared rules that must be copied into several Sites: show copies in the preview, record original-to-new route/target ID mappings, and preserve the meaning of historical diagnostics and references. Do not silently change one route into several independently editable routes.
- [x] Verify representative requests choose equivalent routes and responses before and after migration, including disabled routes/Sites and requests that previously fell through to broader rules. Require an explicit migration decision for behavior that cannot be preserved under Site isolation.
- [x] Update the API, storage constraints, UI, and documentation so all routes require Site ownership after the migration path is complete. Define how older clients are handled instead of silently accepting standalone routes.

### Confirmed publication behavior

New Sites start as drafts until explicitly published. Explicitly saved edits to published Sites continue applying immediately; a full staged-editing system for existing Sites is outside this decision. Per-listener serve/redirect behavior and optional canonical hostnames are agreed requirements and are included above.

| Site state | Ownership on assigned listeners | Site routing outcome |
| --- | --- | --- |
| Draft, either hostname mode | No hostname claims or Default Site slot reserved | Never participates in live routing |
| Published, enabled, specific hostnames | Configured hostname bindings reserved | Serves its routes or configured redirects; path misses stay inside the Site |
| Published, disabled, specific hostnames | Configured hostname bindings remain reserved | Claimed hostnames receive 404; no fallback to another Site |
| Published, enabled, any unmatched hostname | Default Site slot reserved on each assigned listener | Handles hosts unclaimed by specific-hostname Sites |
| Published, disabled, any unmatched hostname | Default Site slot remains reserved | Does not serve unmatched hosts; they receive 404 |
| No assigned listeners, either mode | No listener-local claims or slot reservations | Retained configuration serves no traffic |

Reattaching a published Site validates and applies its bindings immediately; its enabled/disabled state determines the outcome above. Detaching one binding leaves its other bindings intact. These outcomes describe Site routing; existing early request rejection and ACME handling retain their precedence.

### Optional follow-up ideas

- A `Test routing` action accepting a listener and URL and, for HTTPS checks, explicit SNI context. Show the selected Site, route, and matching reason using the runtime matching rules without forwarding a request or advancing load-balancer state.

If standalone routes remain visible during migration, use `Listener-wide (no Site)` and `Host pattern` instead of `Legacy`; avoid calling listener-bound routes `Global`.

## Later phase: Site scope for traffic policies

Agreed as deferred work: start after the required Sites architecture, route workflow, multiple listeners, migration, publication behavior, and readiness summary above are complete and verified. This does not expand the initial Sites implementation.

- [ ] Add a consistent policy applicability selector, such as `Applies to: Global / Selected Sites`, across WAF, rate-limit, traffic-shaper, cache, and retry rules. Keep this distinct from existing cache isolation, budget scope, and protocol scope settings.
- [ ] Define `Global` as eligible for all public traffic reaching the relevant policy stage, including traffic without a matching Site. Define `Selected Sites` as one or more explicit Site IDs, including Default Sites, across their assigned listeners and served hostnames. Scope is combined with the policy's existing match conditions and any route/target restrictions; it does not replace them.
- [ ] Apply Site scope using the resolved Site identity from the validated incoming listener/hostname, not manually duplicated host-pattern expressions. Reuse that identity consistently for policy evaluation, route selection, and diagnostics; cover path misses, disabled published Sites, aliases, listener redirects, and Default Sites without changing established early-rejection or ACME behavior.
- [ ] Preserve and document each policy family's ordering/composition when global and Site-specific rules both match: rate limits remain cumulative, shaper/cache/retry rules retain first-eligible-match selection, and WAF retains its existing ordered action behavior. Scope gates eligibility within the existing priority order; it must not silently introduce a `Site overrides Global` rule. Treat any future change to composition as a separate explicit design decision.
- [ ] Define whether rate-limit and traffic-shaper budgets are shared or isolated across selected Sites/listeners; policy applicability and counter keys are separate concepts. Preserve existing rules' budget behavior during migration.
- [ ] Preserve existing policies as globally eligible with their current match conditions, priorities, and route/target restrictions. Missing/deleted Site selections and empty selected-Site lists must never broaden a restricted policy into Global; require explicit resolution of references.
- [ ] Show globally eligible and explicitly assigned policies in the Site workspace, and support contextual add/edit without leaving it. Preselect the current Site for new Site policies, retain the nested-editor UX, and clearly show other affected Sites when editing a shared policy.
- [ ] Add policy scope to lists and diagnostics, then verify isolation and composition across aliases, shared listeners, Default Sites, Site reassignment, and Site deletion. Keep request matching and migration consistent with the completed Site model.
