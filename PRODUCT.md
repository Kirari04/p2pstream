# Product

## Register

product

## Users

p2pstream serves self-hosters and operators who publish services, inspect live traffic, and troubleshoot a reverse proxy. The management panel must help a first-time operator understand the system without slowing down an experienced operator working under pressure.

## Product Purpose

The product provides one trustworthy place to operate listeners, routes, targets, agents, traffic policy, TLS, environments, and access credentials. The interface succeeds when an operator can understand health immediately, move from a warning to the responsible configuration, and make a safe change without the browser view disagreeing with server state.

## Brand Personality

Calm, exact, capable. The product should feel like a well-made piece of infrastructure: restrained enough to disappear during routine work and explicit enough to inspire confidence during an incident.

## Anti-references

- Generic SaaS dashboards built from interchangeable metric-card grids.
- Decorative terminal or cyberpunk styling that makes routine administration harder to scan.
- Over-rounded, glassy, animated surfaces that obscure state or imply affordances that do not exist.
- Wizard-only administration that hides the system model from experienced operators.

## Design Principles

- Lead with operational truth: health, problems, and the next useful action come before configuration totals.
- Make the system model legible: navigation and labels follow the listener → policy → route → target/agent flow.
- Teach in context: empty states and descriptions help new operators while dense tables and direct links preserve expert speed.
- Confirm server state: mutations reload authoritative data and never leave optimistic configuration presented as fact.
- Reduce surprise: standard controls, consistent states, and clear consequences are more valuable than decorative novelty.

## Accessibility & Inclusion

Target WCAG 2.2 AA. Support keyboard-only operation, visible focus, 200% zoom, reduced motion, system/light/dark appearance, non-color status cues, long translated text, and touch targets appropriate to coarse pointers.

## Security Posture

All values derived from public listeners, requests, routes, targets, agents, headers, traces, and upstream errors are attacker-controlled. Render them as text, constrain them visually without changing their meaning, and keep display, edit, and submitted values consistent. Security review follows the discrepancy-first model.
