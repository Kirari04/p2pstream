# Management UI design system

The management UI uses one visual language, **Calm**, everywhere. It has a
denser **monitor** treatment on the pages operators scan under pressure:
Overview, Monitor → Traffic and Monitor → Diagnostics.

## Principles

- **Flat, not boxed.** The page, sidebar and top bar share one background.
  A page has at most one level of bordered surface. Inside a card, group with
  spacing, hairline dividers or a muted fill (`--app-panel-muted`), never
  another bordered card.
- **One title per thing.** A page has one `PageHeader`. A section has one
  heading. Don't repeat the page title as a card title, and don't stack an
  eyebrow, a heading, a card title and a table caption for the same table.
- **Sentence case.** Every label, button, tab, title, menu item and column
  header: "Save changes", "Add rate-limit rule", "API tokens". Only the first
  word, proper nouns and acronyms (WAF, TLS, API, DNS, ACME…) are capitalized.
  No uppercase eyebrows. "Management TLS" keeps its name.
- **One accent.** Blue (`--app-accent`) is for the primary action, links,
  focus and the current selection. Status colors (`--app-success`,
  `--app-warning`, `--app-error`) only ever mean status.
- **Quiet empties.** When a value is unavailable, show `—` with a short
  sentence-case reason ("No traffic yet"). Don't show `0 B/s` noise or lone
  dashes.
- **Numbers are tabular.** Use `font-variant-numeric: tabular-nums`. Use the
  monospace font only for identifiers (IDs, fingerprints, versions, hosts) and
  for values in monitor tiles.

## Tokens

All colors come from the CSS custom properties in `src/styles.css` (light and
dark) and the Naive UI overrides in `src/theme/naive.ts`. Never hard-code hex
values in components.

| Token | Use |
| --- | --- |
| `--app-bg` / `--app-shell` | page, sidebar and top bar background |
| `--app-panel` | the single raised surface (cards, drawers, tables) |
| `--app-panel-muted` | inset groups inside a card, monitor tiles, code blocks |
| `--app-border-subtle` | default hairline: card borders, dividers, table rows |
| `--app-border` | input borders and stronger separators only |
| `--app-text` / `--app-text-muted` | primary and secondary text |
| `--app-hover` | hover and selected-row backgrounds |

Radii: 10px for cards, 8px for inner groups, tiles and inputs, 6px for chips.

## Type scale

| Role | Size / weight |
| --- | --- |
| Page title (`PageHeader`) | 1.375rem / 600 |
| Section title (`SectionCard`) | 0.9375rem / 600 |
| Body | 0.875rem |
| Secondary / descriptions | 0.8125rem, muted |
| Meta, table detail | 0.75rem, muted |
| Metric value | 1.375rem / 600 (strip); 1.125rem mono (tiles) |

## Building blocks (`src/components/ui`)

- **`PageHeader`** (`title`, `description`; slots `badge`, `description`,
  `actions`): the `h1` of every routed page. The page's primary action goes
  rightmost in `actions`. On pages with tabs, the primary action follows the
  active tab (Traffic policy → "Add rate-limit rule", "Add WAF rule", …);
  list toolbars only hold filters.
- **`SectionCard`** (`title`, `description`, `flush`, `headingId`,
  `variant="card"|"plain"`; slots `badge`, `actions`, `footer`): every
  content section. Use `flush` for edge-to-edge tables. Use
  `variant="plain"` for a headed group inside another surface.
- **`MetricStrip`** + **`MetricCard`** (`label`, `value`, `detail`,
  `tone`, `dot`, `textValue`, `wrapDetail`): key numbers. Set `textValue` when the value
  is a word such as "Running" so tiles keep it in the sans font. Use `variant="strip"` on Calm pages and
  `variant="tiles"` on monitor pages.
- **`SegmentedControl`** (`v-model`, `options`, `label`): small mutually
  exclusive pickers such as time windows. It renders buttons with
  `aria-pressed` inside a labelled group.
- **`.command-block`** (global class on a `pre`): shell commands and
  one-time snippets.
- **`useNarrowScreen()`** (`src/composables/useMediaQuery.ts`): the
  phone breakpoint (below 640px) for layouts that must change in script,
  for example table columns.
- `EmptyState`, `DisabledHint`, `AccessibleSelect`: unchanged.

## Layout

- The main column is capped at 88rem and centered; it doesn't grow on
  ultrawide screens.
- **Tabs vs. sidebar:** use page tabs only when the tabs share one page
  header (Proxy, Agents, Traffic policy, Diagnostics). Pages with their own
  header (Settings pages, Monitor → Traffic flow / Diagnostics) are reached
  from the sidebar and breadcrumbs, with no tab bar repeating the sidebar.

## Patterns

- **Tables:** `NDataTable size="small"` inside a flush `SectionCard`
  (row hairlines only; don't set `single-line=false`). Responsive `role="table"`
  grids that stack into labelled cells on phones, as used by the Traffic
  Policy rule lists, are an accepted alternative. Set
  `scroll-x` to the real column sum, keep it within the card at 1280px, pin
  the actions column with `fixed: "right"`, and shorten long IDs with
  `clip-text` plus a `title` attribute.
- **Row actions:** at most one inline action that needs attention, then
  Edit. Put everything else, including Delete, in a "More" menu
  (`aria-label="More actions for …"`). Destructive buttons outside menus use
  `type="error" secondary` with a text label.
- **Field-row remove buttons** inside editors show the icon plus a visible
  "Remove" label, with a specific `aria-label` ("Remove response header").
- **Editors:** a right-hand `NDrawer` with `editor-drawer` and
  `editorDrawerWidth()`, sticky `editor-drawer-actions` (Cancel, then
  primary).
- **Modals:** only for confirmations and one-shot reveals. Always use
  `modalCardStyle()` plus `modalScrollableContentStyle()`.
- **Status:** `NTag size="small" :bordered="false"` with a semantic type, or
  `MetricCard dot` for headline status.
- **Diagrams:** show what a node is with a neutral muted icon, never a color.
  Color on a node means live status only (cache hit, agent offline).

## Accessibility and test contracts

Keep existing accessible names, `aria-*` attributes, `data-testid` values and
visible button labels that the Playwright specs rely on (`e2e/`,
`e2e-isolated/`, `docs-screenshots/`). If you rename one, update the spec in
the same change.

## Untrusted text

Hostnames, paths, headers, error details, agent names and anything else
derived from public traffic or remote environments are attacker-controlled.
Keep rendering them through `diagnosticExcerpt` / `diagnosticInspectionText`
(`src/lib/diagnosticText.ts`) inside `<bdi dir="ltr">` where the code already
does. A redesign must never swap those for raw interpolation or `v-html`.
