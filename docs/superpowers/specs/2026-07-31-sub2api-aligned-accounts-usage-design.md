# Sub2API-Aligned Accounts And Usage Design

## Status

- Date: 2026-07-31
- State: approved design brief, implementation pending
- Worktree: `codex/uiux-display-contract`
- Integration rule: remain isolated from `main`

## Goal

Align the Admin "客户与计费账户" surface with Sub2API's dense user-table interaction pattern, and align customer request usage with Sub2API's essential request facts. The implementation must preserve OPL's authority boundaries instead of forwarding raw Sub2API admin objects.

The four mandatory request facts are:

- Token
- 费用
- 延迟
- 时间

## Visual Source

The source is Sub2API `v0.1.162`, resolved to commit `27f094e0960ebd8e52de7ff7e763c6fec2ff4057`:

- `frontend/src/views/admin/UsersView.vue`
- `frontend/src/views/user/UsageView.vue`
- `frontend/src/components/admin/usage/UsageTable.vue`

OPL adopts the information density, table hierarchy, compact status treatment, and stacked Token/latency cells. It does not copy Sub2API's Vue implementation, upstream-provider Account semantics, color theme, raw DTOs, or unsupported controls.

The existing OPL Quiet Ledger visual system and the two immutable ImageGen reference files remain unchanged.

## Object Boundary

"客户与计费账户" represents this one-to-one owner graph:

```text
Console User -> OPL Account -> Sub2API User / Wallet
```

It does not represent a Sub2API upstream provider Account. Therefore, provider platform, scheduling, proxy, capacity, priority, upstream billing rate, and provider expiry fields remain forbidden on this page.

## Admin Accounts Surface

### Desktop

Replace the current two-column account-card grid with one full-width, scan-first table. Keep the existing page title, provisioning action, pagination, account detail, wallet adjustment, and disable workflows.

The default columns are:

1. 用户: email as the primary value, with owner/admin role as secondary status.
2. 账户映射: OPL Account ID, Console User ID, and Sub2API User ID in a compact stacked cell.
3. 余额: spendable USD balance and wallet status from the nested wallet source.
4. API 费用: today and cumulative actual cost from the nested usage source.
5. 资源: live Key count and Control Plane Workspace count.
6. 状态: active/disabled with the established semantic indicator.
7. 操作: detail, wallet adjustment, and disable commands using existing authorization and confirmation flows.

Nested source diagnostics move out of the default table. They remain available in account detail so operator truth is not removed, only demoted from the primary scanning path.

No search, sorting, last-active time, created time, or filter control is added until `GET /api/operator/accounts` exposes the matching server contract. Browser-only fake filtering is forbidden.

### Mobile

Keep one card per account, but use the same information order as desktop. User identity, balance, API cost, resource counts, status, and actions must fit without horizontal scrolling or overlapping controls.

## Customer Usage Surface

### Desktop

Keep the existing API Key selector, period control, authoritative usage summary, pagination, source states, and retry behavior. Replace the request table with this fixed column order:

1. 模型 / 端点
2. Token
3. 费用
4. 延迟
5. 时间
6. 请求 ID

Token is one stacked cell containing authoritative fields only:

- input Token
- output Token
- cache read Token when nonzero
- cache creation Token when nonzero

费用 displays `actualCostUsdMicros` only. It does not derive standard cost, account cost, or price multipliers in the browser.

延迟 is one stacked cell matching Sub2API's semantics:

- 首字: `firstTokenMs`
- 总耗时: `durationMs`

A missing upstream latency value renders `-`. It must never render as `0 ms` and must not be inferred from browser timing, request timestamps, Token counts, or response arrival.

时间 displays `createdAt`. Request ID remains visible for support correlation but is visually secondary to the four mandatory facts.

### Mobile

Each request becomes a compact row/card with four stable fact groups: Token, actual cost, latency, and time. Model and endpoint provide context; request ID is secondary and copyable/readable without expanding the card width.

## Data Flow

```text
Sub2API GET /api/v1/admin/usage
  -> Sub2APIHTTPClient strict usage-row decoder
  -> Control Plane GET /api/gateway/keys/{keyId}/usage
  -> customer-safe SourceEnvelope
  -> React GatewayUsageItem
  -> UsagePage
```

The request usage projection adds only:

- `duration_ms` -> `durationMs`
- `first_token_ms` -> `firstTokenMs`

Both fields are nullable non-negative integers. Control Plane validates them and continues to reject identity mismatch, malformed pagination, negative values, raw response content, prompts, secrets, and unsupported fields. OPL persists no usage or latency copy.

The Admin account table uses the existing `GET /api/operator/accounts` projection without widening its DTO.

## Contract Changes

Update these current truths together with code and tests:

- `docs/invariants.md`
- `packages/contracts/opl-cloud-launch-freeze-contract.json`
- `packages/contracts/opl-cloud-console-source-truth-contract.json`
- `packages/contracts/opl-cloud-console-ui-contract.json`
- `docs/product/console-display-contract-v1.md`

The usage field allowlist must add `duration_ms` and `first_token_ms` upstream and `durationMs` and `firstTokenMs` in the customer DTO. The UI contract must freeze Token, cost, latency, and time as required request-record facts.

## Source And Error States

- `available`: render authoritative records and nullable latency values.
- `empty`: render the existing no-request state.
- `unavailable`: render no fallback rows or zero values and keep retry available.
- Partial latency: render the available value and `-` for the missing value.
- Account nested-source failure: only the affected cell becomes unavailable; other account facts remain visible.

## Accessibility And Interaction

- Preserve semantic table headers on desktop and list roles on mobile.
- Keep all existing modal focus management and confirmation requirements.
- Use the existing Lucide icons and Apps SDK UI-backed controls.
- Do not encode latency health using color alone; labels and numeric values remain present.
- Table density must not reduce target sizes for account commands below the existing control minimum.

## Tests And Evidence

Required focused evidence:

- Sub2API client accepts nullable non-negative latency fields and rejects negative values.
- Control Plane usage DTO exposes only the expanded strict allowlist.
- Source-truth and launch contracts include the two latency fields.
- React request rows always expose Token, cost, latency, and time on desktop and mobile.
- Missing latency renders `-`, never zero.
- Admin desktop uses one dense table; mobile keeps the compact account list.
- Existing detail, wallet adjustment, disable, Key selection, period selection, pagination, retry, and source-state interactions still work.
- Fake-only browser QA reports zero external requests, page errors, and console errors.
- Desktop and mobile screenshots cover both updated surfaces and are compared against the Sub2API source structure plus the existing Quiet Ledger system.

Final gates remain the full repository tests, typecheck, unused-code lint, production build, JSON validation, diff checks, and immutable image hash checks.

## Non-Goals

- No direct browser call, link, iframe, or redirect to Sub2API.
- No raw Sub2API DTO forwarding.
- No second usage store or latency database.
- No provider Account fields on customer billing accounts.
- No new search, sort, export, chart, or column-preference system.
- No ImageGen regeneration and no mutation of either frozen PNG.
- No merge into `main`.
