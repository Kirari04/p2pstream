# p2pstream Management Design System

## Direction

A health-first operations console with Cloudflare's operational clarity and Linear's precision. The color strategy is restrained: neutral working surfaces, p2pstream blue only for navigation and primary actions, and semantic color only when it communicates state. The approved north-star is the light health-first command-center direction, with an equally complete dark theme.

## Scene

An operator at a desk moves between daylight setup work and low-light troubleshooting, focused on finding the source of a problem without losing context; first visit follows the operating-system theme and an explicit preference remains available.

## Typography

- IBM Plex Sans is the single UI family; IBM Plex Mono is reserved for addresses, identifiers, paths, snippets, and aligned technical values.
- Use a fixed product scale: 0.75rem metadata, 0.875rem secondary UI, 1rem body/control copy, 1.125rem section headings, and 1.5rem page headings.
- Use regular for body, medium for controls, semibold for hierarchy, and tabular numerals for operational data.
- Body copy uses at least 1.5 line height and explanatory text stays within 70ch.

## Color

- Light surfaces: cool near-white workspace, white content, cool-gray navigation layer, dark navy ink.
- Dark surfaces: near-black workspace, charcoal content/navigation layers, near-white ink.
- Brand blue communicates current location, focus, links, and primary action only.
- Green, amber, red, and blue semantic roles always pair color with an icon, label, or status text.
- Text and controls must meet WCAG AA in both themes; placeholder copy receives body-text contrast.

## Layout

- Desktop uses a collapsible left sidebar, compact top bar, and a fluid content canvas capped for readable composition rather than a centered marketing container.
- Sidebar groups are Observe, Configure, and System. Tablet uses an icon rail; mobile uses a modal navigation drawer without removing any destination.
- Use a 4px spacing base with 8–12px inside controls, 16–24px inside sections, and 32px between major page regions.
- Prefer dividers, alignment, and surface changes over wrapping every group in a card. Never nest cards.

## Components

- Page headers establish location, short context, and one primary action.
- Status summaries are compact horizontal facts, not hero metrics.
- Attention rows explain what happened and link to the existing surface that can verify or fix it.
- Tables keep their semantic columns, support horizontal scrolling, and move secondary row actions into a consistent action cluster.
- Substantial configuration uses a right-side editor drawer on desktop and a full-screen editor on mobile. Short confirmations and secret reveals remain dialogs.
- Empty states state why the area is empty and provide the next safe action. Loading uses skeletons shaped like the eventual content.

## Interaction & Motion

- State transitions use 150–200ms ease-out motion; no page-load choreography or decorative movement.
- Focus is never removed. Drawers and dialogs trap focus and restore it to their trigger.
- Hover adds confirmation but never carries unique functionality. Coarse pointers receive at least 44px hit targets.
- Destructive actions name the object and consequence; disabled actions expose the reason.
- Respect `prefers-reduced-motion` by removing nonessential transforms and shortening transitions.

## Content

- Voice is concise, calm, and specific. Use “Log in,” “Create route,” “Save changes,” and “Delete listener,” not generic “Continue,” “Submit,” or “OK” where the outcome can be named.
- Errors state what happened and what the operator can do next without blaming them.
- Use Listener, Route, Target, Agent, Traffic Policy, Environment, and API Token consistently.

## Security and Data Rendering

- Never use `v-html` or DOM HTML insertion for runtime/server values.
- Preserve hostile and unusually long values as escaped text; use wrapping, clipping, and explicit disclosure for layout safety.
- Do not construct executable links from listener-controlled values.
- After mutations, present refreshed server data as authoritative and surface discrepancies as errors rather than silently normalizing the display.
