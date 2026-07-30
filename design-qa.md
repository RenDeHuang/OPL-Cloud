# OPL Cloud React Console Design QA

## Comparison Target

- Source visual truth: `/tmp/opl-uiux-v3-directions-live.png` and the selected Fresh Focus crop `/tmp/v3-direction-v2-1.png`.
- Implementation screenshots: `/tmp/opl-console-react-qa-final-v3`.
- Full-view comparison evidence:
  - `/tmp/opl-design-qa-desktop-comparison.png`
  - `/tmp/opl-design-qa-mobile-comparison.png`
  - `/tmp/opl-design-qa-primary-desktop.png`
  - `/tmp/opl-design-qa-primary-mobile.png`
- Focused region comparison evidence: `/tmp/opl-design-qa-focused-comparison.png`.
- Viewports: desktop `1440x1024`; mobile `390x844`.
- State: authenticated customer and operator fixtures, authoritative sources available unless the captured page intentionally demonstrates empty state; wallet modal filled but not submitted on mobile.

The selected direction is a visual system target rather than a pixel-complete screen specification. It fixes the quiet left rail, flat grouped bands, thin separators, restrained cobalt/green accents, compact typography, and scan-first density. The Chinese copy, source metadata, additional Admin facts, and exact 10-page/27-slide content come from the frozen display contract and are intentional product constraints. The source board has no separate mobile mock, so mobile is evaluated as a responsive translation of the same hierarchy and tokens.

## Findings

No actionable P0, P1, or P2 findings remain.

- Fonts and typography: the implementation uses the repository system sans and mono stacks, compact 11-16px operational text, tabular numeric emphasis, zero letter spacing, and stable wrapping/truncation. Headings remain proportional to dense Console surfaces rather than becoming marketing-scale display text.
- Spacing and layout rhythm: desktop preserves the selected direction's fixed rail, broad content canvas, grouped horizontal bands, and light separators. Mobile converts wide tables into stable object cards, keeps the five primary destinations reachable, and has no viewport overflow at `390x844`.
- Colors and visual tokens: neutral surfaces carry most grouping; cobalt is limited to navigation and primary actions, green to healthy/available state, navy to authoritative overview bands, and red to destructive/error state. There are no gradients, decorative blobs, or one-note purple/beige styling.
- Image quality and asset fidelity: the product uses the existing real OPL bitmap mark and `lucide-react` icons. The selected Console direction contains no required product photography or illustration, so adding generated raster art would reduce fidelity and scanning density. No visible target asset was replaced with CSS art, text glyphs, emoji, or handcrafted SVG.
- Copy and content: all 10 primary pages and 27 frozen slides use the approved Chinese product vocabulary. Authority labels distinguish Control Plane, Sub2API, Fabric, and Ledger; `available`, `empty`, and `unavailable` remain distinct; technical `paidThrough` copy is replaced by `权益截止`.
- Icons and controls: navigation, refresh, settings, support, reveal/copy, status, and command actions use one icon family. Apps SDK UI buttons and fields retain accessible focus, disabled, busy, invalid, and modal states through the local thin adapters.
- Responsiveness and accessibility: Browser QA covers desktop/mobile navigation, keyboard selection, modal focus behavior, source states, secret cleanup, and horizontal overflow. The `390x844` wallet modal footer keeps both actions inside the footer and gives them equal available width.

Accepted visual deviations from the direction board are intentional: the implementation adds the frozen source-truth metadata and Admin operational density, and uses a navy authority band on overview/system summary surfaces. These preserve the Fresh Focus hierarchy while making authority boundaries and operational state unambiguous.

## Patches Made

- Replaced mobile Admin account, resource, resource-detail, and system wide tables with object cards while retaining every required fact and action.
- Restored business-first Workspace labels and removed the raw `paidThrough` presentation.
- Separated source value, authority, availability, and readback time into scannable layers.
- Added complete empty-state spacing and hierarchy.
- Removed the legacy global input border from Apps SDK fields and fixed the wallet amount input's double border.
- Preserved caller classes in the Apps SDK Button adapter so Console sizing, color, radius, and focus styles apply to real library buttons.
- Added mobile API Key cards with the same reveal, copy, lifecycle, quota, usage, and action protections as desktop.
- Fixed the `<=520px` modal footer to keep both actions on one row with equal flexible width.
- Extended fake-only Browser QA to capture the wallet modal on both viewports and fail when any footer action crosses its container boundary.

## Verification Evidence

- Browser acceptance: `10/10` passing against `/tmp/opl-console-react-qa-final-v3`.
- Full Node suite: `384/384` passing, with `0` failures and `0` skips.
- Typecheck and unused-code lint: passing.
- Production Vite build: passing; the existing manual chunk split emits a non-blocking circular-chunk warning and no build error.
- Vue retirement scan: no tracked `.vue` files remain.
- CodeGraph: synchronized after the final React changes.
- Browser network: `fake-only`; external requests `0`.
- Browser errors: no page errors and no unexpected Console errors.
- Idempotency: Gateway Key write `1`; wallet adjustment write `1`.
- Primary page evidence: all 10 primary pages captured at both required viewports.
- Focused evidence: mobile account, resource, system, API Key, and wallet modal states compared together with the selected direction.

## Follow-up Polish

No P3 item is required for acceptance. Future visual refinement should remain within the frozen Fresh Focus direction and must not add decorative media or hide source-truth metadata.

final result: passed
