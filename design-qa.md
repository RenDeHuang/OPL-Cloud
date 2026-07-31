# OPL Cloud React Console Design QA

## Comparison Targets

### Console visual system

- Source visual truth: `output/imagegen/opl-console-option-1-quiet-ledger-1440x1024.png`
- Source SHA-256: `9b75ba8b01cda552fcb7d44bc774797f22697f5fd4a0c067e885bc4895b738b5`
- Desktop implementation: `output/browser-qa/quiet-ledger/fixture-console-overview-desktop.png`
- Mobile implementation: `output/browser-qa/quiet-ledger/fixture-console-overview-mobile.png`
- Full comparison: `output/browser-qa/quiet-ledger/quiet-ledger-overview-comparison.png`
- Focused comparison: `output/browser-qa/quiet-ledger/quiet-ledger-overview-focus-comparison.png`

### Workspace Split Decision

- Source visual truth: `output/imagegen/opl-workspace-launch-option-1-split-decision-1440x1024.png`
- Source SHA-256: `897f657539d2ccd8e10df365e5108094bee3a68ffd1c349190f692501657b4b6`
- Desktop implementation states:
  - `output/browser-qa/split-decision/fixture-workspace-new-ready-desktop.png`
  - `output/browser-qa/split-decision/fixture-workspace-confirm-desktop.png`
  - `output/browser-qa/split-decision/fixture-workspace-operation-desktop.png`
- Mobile implementation states:
  - `output/browser-qa/split-decision/fixture-workspace-new-ready-mobile.png`
  - `output/browser-qa/split-decision/fixture-workspace-confirm-mobile.png`
  - `output/browser-qa/split-decision/fixture-workspace-operation-mobile.png`
- Viewports: desktop `1440x1024`; mobile `390x844`
- State: authenticated customer fixture; catalog, pricing preview and wallet available; configuration filled and actionable; confirmation checked; operation `preparing/runtime_starting`
- Full-view comparison: `output/browser-qa/split-decision/split-decision-configure-full-comparison.png`
- Focused region comparison: `output/browser-qa/split-decision/split-decision-configure-focus-comparison.png`
- Three-state evidence: `output/browser-qa/split-decision/split-decision-desktop-flow.png` and `output/browser-qa/split-decision/split-decision-mobile-flow.png`

The source and rendered implementation were opened together in the full and focused comparison artifacts. The focused comparison removes the shared shell and keeps the steps, decision surface, plan rows and order summary readable. The flow artifacts compare configuration, confirmation and operation states together.

### Sub2API-aligned accounts and usage

- Customer usage desktop: `output/browser-qa/sub2api-alignment/fixture-api-usage-desktop.png`
- Customer usage mobile: `output/browser-qa/sub2api-alignment/fixture-api-usage-mobile.png`
- Admin accounts desktop: `output/browser-qa/sub2api-alignment/fixture-admin-accounts-desktop.png`
- Admin accounts mobile: `output/browser-qa/sub2api-alignment/fixture-admin-accounts-mobile.png`
- Viewports: desktop `1440x1024`; mobile `390x844`
- Usage hierarchy: model/endpoint, Token, actual cost, latency, time and request ID.
- Account hierarchy: user, account mapping, balance, API cost, resources, status and actions.

The four captures were inspected at original resolution. Desktop tables remain scan-first without clipped identifiers or actions. Mobile cards preserve the same fact order, wrap identifiers within the content width and keep controls clear of the fixed navigation.

## Findings

No actionable P0, P1 or P2 findings remain.

### Required fidelity surfaces

- Fonts and typography: both directions use compact sans-serif UI typography, tabular money, zero letter spacing and a clear title/label/value hierarchy. Long names, price versions and operation IDs wrap without resizing controls.
- Spacing and layout rhythm: desktop preserves the 240px rail, open canvas, left decision surface and 320px sticky summary. Confirmation keeps the same split instead of becoming another stacked page. Mobile uses one column, keeps the summary after the decision content and has no horizontal overflow.
- Colors and tokens: neutral surfaces carry the layout; deep green is limited to selection, primary actions and healthy state. Disabled, unavailable, warning and error states keep semantic contrast. No gradient, decorative blob or dark hero is present.
- Image quality and asset fidelity: both immutable ImageGen PNGs retain their recorded hashes. The selected Workspace target contains no separate photographic or illustrative assets. The implementation uses the existing OPL bitmap mark and the established icon library; no target asset is replaced by handwritten SVG, emoji or placeholder art.
- Copy and content: the UI uses customer-facing product language and omits source-boundary teaching copy. The three steps describe browser navigation only. Operation content names only the returned status and current phase.
- Icons and controls: Apps SDK UI supplies the plan RadioGroup, name Field and confirmation Checkbox. The checked state is visibly stable before capture. Buttons, refresh, navigation and status actions retain accessible names, focus and disabled states.
- States and responsiveness: desktop and mobile evidence covers available, unavailable, selected, checked, disabled and active-operation states. The selected plan remains visually explicit, an unavailable plan cannot remain selected, confirmation returns to the top, and the operation page contains no inferred progress rail.
- Usage controls and records: the API Key selector and period control use full-width Apps SDK-compatible block layout. Desktop exposes the frozen six-column table; mobile exposes the same facts in compact cards. Missing latency is `-`, never an invented zero, while authoritative zero remains `0 ms`.
- Admin accounts: desktop exposes the frozen seven-column hierarchy and keeps all actions readable in a stable table cell. Mobile keeps the same hierarchy and command set without browser-side search or sort semantics.

### API truth audit

- `GET /api/pricing/catalog` owns package identity, availability, CPU, memory and storage shape.
- `POST /api/pricing/preview` owns compute charge, storage charge, total, price version, currency and `calendar_month` billing unit.
- `GET /api/gateway/wallet` owns the spendable balance and source availability. The browser guard requires balance strictly greater than the preview total; it remains advisory and the server rechecks.
- `POST /api/workspace-launches` accepts only the reviewed name, package, size and `autoRenew=false`; browser pricing fields are not submitted.
- `GET /api/workspace-launches/{operationId}` owns operation ID, status, current phase, pricing readback, timestamps and error code.
- `GET /api/gateway/keys/{keyId}/usage` owns each request's model, endpoint, Token breakdown, actual cost, nullable first-token/total latency, time and request ID. The browser does not derive latency or cost.
- `GET /api/gateway/keys/{keyId}/usage-summary` owns the selected Key aggregate; `GET /api/gateway/usage-summary` owns account-wide aggregate values. Neither summary is calculated from the visible page.
- `GET /api/operator/accounts` owns the Admin account projection. Control Plane owns account/user/role/status/workspace mappings; Sub2API readback owns gateway identity, wallet, Key count and today/cumulative API usage.
- Region, provider capacity, ETA, inferred completed phases and post-debit balance have no customer API fields and are absent.

## Patches Made Since Previous QA

- Replaced the crowded launch form with the selected Split Decision composition and responsive one-column translation.
- Replaced custom plan selection with Apps SDK UI RadioGroup and retained Apps SDK UI Field and Checkbox adapters.
- Fixed the confirmation checkbox width collapse that caused per-character wrapping, then captured its settled checked state.
- Fixed mobile confirmation scroll position, unavailable-plan selection and radio visibility/shape.
- Removed the old inferred phase progression; only authoritative `status` and current `phase` remain.
- Corrected browser fixtures to the real preview DTO: `billingUnit=calendar_month` with separate compute and storage components.
- Aligned the browser balance guard with the backend invariant: equal balance is insufficient; only a strictly greater balance enables submission.
- Aligned customer usage with Sub2API request records: Token input/output/cache details, actual cost, first-token/total latency, time and request ID now appear in the same scan path.
- Aligned Admin accounts with the Sub2API account-table hierarchy while preserving the existing Control Plane API boundary and account commands.
- Fixed the Apps SDK selector widths and the Admin action-cell layout so desktop and mobile controls no longer compress or clip.

## Accepted Deviations

- The ImageGen target specifies the configuration state only. Confirmation and operation use the same visual system plus the frozen display and API contracts.
- Fixture names differ from the mock's sample copy; layout and state are matched.
- The established Console places the return action above the flow instead of duplicating it inside the sticky summary.
- No mobile source mock exists; mobile is evaluated as the responsive translation of the selected desktop direction.

## Verification Evidence

- Fake-only browser QA: passed for desktop and mobile, customer and operator roles, all primary pages, source-state recovery, six-field request records, seven-column Admin accounts, key lifecycle, secret cleanup and zero external requests.
- High-risk command replay: Workspace launch, operator account provisioning, announcement create, publish and withdraw, and Support mapping each drop the first response after the fake write is applied. The second UI action reuses the same `Idempotency-Key`; Browser QA proves two HTTP attempts produce one stable business mutation and then confirms the result through the corresponding authoritative GET readback.
- Browser console errors: `0`; page errors: `0`; external requests: `0`.
- Full repository test: `npm test` passed `420/420`, with `0` failures, `0` cancelled and `0` skipped.
- Control Plane test: PostgreSQL-backed `go test ./... -count=1` passed with `OPL_POSTGRES_TESTS=1` and `OPL_CAPACITY_TESTS=1`; the longest `internal/server` package completed in `417.487s`.
- TypeScript gate: `npm run typecheck` passed with no diagnostics.
- Unused-code lint gate: `npm run lint` passed with no diagnostics.
- Production build: `npm run build` passed after transforming `2526` modules. Vite reported the existing `vendor -> react-ui -> vendor` manual-chunk cycle warning; production assets were still emitted successfully.

## Follow-up Polish

No P3 item is required for this handoff. Later visual changes must preserve both immutable reference images and the API field audit above.

final result: passed
