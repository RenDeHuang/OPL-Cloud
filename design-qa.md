# OPL Console Design QA

## Comparison Target

- Product checkpoint: `5fc1f7ab364d8e34b851d9c5c467ddcda88d9352`.
- Source visual truth:
  - `/home/dev/.codex/generated_images/opl-gateway-usage-20260717.png`
  - `/home/dev/.codex/generated_images/opl-gateway-api-keys-unified-shell-20260717.png`
  - `/home/dev/.codex/generated_images/opl-admin-operations-20260717-v2.png`
- Implementation screenshots:
  - `/home/dev/medopl-3/output/design-qa/gateway-usage-desktop.png`
  - `/home/dev/medopl-3/output/design-qa/gateway-keys-desktop.png`
  - `/home/dev/medopl-3/output/design-qa/admin-overview-desktop.png`
  - `/home/dev/medopl-3/output/design-qa/gateway-usage-tablet.png`
  - `/home/dev/medopl-3/output/design-qa/gateway-usage-mobile.png`
  - `/home/dev/medopl-3/output/design-qa/admin-overview-mobile.png`
- Full-view and focused comparison evidence: `/home/dev/medopl-3/output/design-qa/comparison.png`
- Desktop viewport: `1440x900`; responsive checks: `768x1024` and `375x812`.
- State: real local Control Plane session and API routes. Admin projections returned `200`; local Gateway upstream returned `502`, so Gateway screenshots show the implemented unavailable state rather than invented usage or Key data.
- Artifact policy: `output/` contains local generated screenshots and comparison files. It is intentionally ignored and is not part of the product commit.

## Findings

- No actionable P0, P1, or P2 findings remain.
- The ordinary Console navigation remains visible for the administrator, with an additive operations section.
- Gateway Usage and API Keys preserve the reference hierarchy, grouped metrics, tabs, tables, pagination, and unavailable states.
- Admin omits the reference's "latest verification" row because the current API has no trustworthy production E2E fact.

## Fidelity Surfaces

- Fonts and typography: existing Inter/system stack, weights, line heights, wrapping, and zero letter spacing are consistent across all routes.
- Spacing and layout: sidebar, tabs, grouped metrics, tables, and admin panels match the reference density; no page-level overflow at tested viewports.
- Colors and tokens: existing blue, neutral, success, and error tokens are reused; no new palette or decorative gradient was introduced.
- Image and icon quality: existing OPL logo and installed Lucide icon family are used; no placeholder, custom SVG, or CSS illustration was added.
- Copy and content: customer-facing internal terms remain removed; readiness is labeled as dependency status rather than production verification.

## Patches Made

- Cleared revealed Gateway Key state when leaving the API Keys route.
- Cleared any previously revealed Key before refresh or reveal requests so a failed request cannot leave plaintext on screen.
- Removed the redundant Gateway summary error from Usage while keeping independent stats and list retries.
- Renamed readiness rows to "运行依赖 / 生产依赖" to match the actual endpoint semantics.
- Grouped Gateway Usage metrics into one summary surface and aligned the period control with the reference.
- Verified the mobile sidebar after its 180ms transition; its final bounds are `0-292px` with no clipping.

## Follow-up Polish

- P3 only: the reference's decorative empty-state tray icon is intentionally omitted; the real empty/error copy remains clear without adding nonessential decoration.

final result: passed
