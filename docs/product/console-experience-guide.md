# OPL Console Experience Guide

Owner: `OPL Console`

Purpose: `evolvable_product_experience_guidance`

State: `current_truth`

This guide defines the user outcomes expected from the generic OPL Cloud
Console. It is not a visual freeze or a machine contract. Current routes,
components, layout and styling are owned by `apps/console-ui`; product facts
and API authority remain with their domain owners.

## Product Outcome

The Console should make a signed-in user understand, without architecture
jargon:

1. that this is OPL Cloud;
2. which Workspace, AI API, balance and billing tasks are currently available;
3. whether each displayed fact is available, empty or unavailable;
4. what action can be taken next and what the result was.

Public entry and login surfaces introduce the generic OPL Cloud product. They
must not hard-code medopl or another instance identity; instance branding and
deployment facts belong to the instance owner.

## Experience Principles

- Prefer user tasks and outcomes over internal service, contract or authority
  vocabulary.
- Keep operational surfaces professional, coherent and efficient for repeated
  work. Visual quality is evaluated in the context of the current product and
  viewport, not by conformance to a historical palette or screenshot.
- Use a clear responsive hierarchy. All supported actions and critical facts
  must remain reachable on desktop and mobile.
- Make loading, empty, unavailable, partial-failure and permission states
  explicit. Never present an unavailable fact as zero, empty or healthy.
- Keep independent sources independently usable. A failed balance read must not
  hide a valid Workspace list, for example.
- Use accessible semantics, keyboard interaction, focus management, readable
  contrast and non-color status cues.

## Truth And Safety

- The browser calls only Control Plane product APIs. It does not call Fabric,
  Ledger, Sub2API or provider APIs directly.
- Display service-provided money, time, status and price snapshots without
  inventing business truth in the browser.
- Do not synthesize trends, capacity, health, ETA or completion facts when the
  owning API does not provide them.
- Passwords and keys are hidden by default. Reveal requires an explicit owner
  action, uses a private/no-store response and must not persist in browser
  storage, logs, receipts or ordinary page state beyond the active sensitive
  interaction.
- Do not expose internal provider cost, credentials, raw downstream admin DTOs
  or management endpoints.

The canonical field, source and permission details live in the public API
schemas and eligible contracts under `packages/contracts`; this guide does not
copy their field lists.

## Design Freedom

No long-term product rule fixes:

- a green, blue or other palette;
- gradients, shadows or a particular card treatment;
- sidebar width, summary width, grid ratio or breakpoint;
- page, tab, route, slide or column count;
- React, a component library, an icon set or a design-assistance model;
- an immutable screenshot or image hash.

Those choices should be made against the current content, brand, accessibility,
device and user workflow. The owning implementation may evolve them without a
contract revision. A product-level update is needed only when the user outcome,
authority boundary or durable experience principle changes.

## Capability And Currentness

Functional module documents describe intended user capabilities. Source and
API schemas describe current implementation. `docs/status.md` reports current
evidence, while `docs/roadmap.md` owns missing capabilities and acceptance
outcomes. A visual treatment must not imply that a roadmap target is already
available.

## Verification

For a meaningful Console change, verify the affected user path at representative
desktop and mobile viewports, plus focused behavior, accessibility and source-
truth tests. Screenshots are review evidence, not immutable product contracts.
Do not add exact CSS-value or image-hash assertions unless a separate legal,
brand-integrity or safety requirement specifically justifies them.
