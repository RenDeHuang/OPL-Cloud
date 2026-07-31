# Sub2API-Aligned Accounts And Usage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Admin account list scan like Sub2API's user table and make every customer usage row expose authoritative Token, actual cost, latency, and time facts.

**Architecture:** Keep the browser on Control Plane APIs only. Extend the strict Sub2API usage projection by two nullable non-negative latency fields, carry those fields through the customer-safe DTO, and render the frozen seven-column account table plus six-column usage table without widening the operator account API.

**Tech Stack:** Go 1.24 Control Plane, React 19, TypeScript, `@openai/apps-sdk-ui`, Lucide, Node test runner, Playwright browser QA.

**Worktree rule:** Execute only in `/home/dev/medopl-3/.worktrees/uiux-display-contract` on `codex/uiux-display-contract`. Preserve existing dirty changes. Do not commit, merge, regenerate, edit, or overwrite either frozen ImageGen PNG.

---

## File Map

- `services/control-plane/internal/clients/sub2api.go`: strict upstream usage decoder and internal usage record.
- `services/control-plane/internal/server/routes_gateway.go`: customer-safe Control Plane projection.
- `apps/console-ui/src/api/dtos.ts`: React usage DTO.
- `apps/console-ui/src/pages/CustomerPages.tsx`: desktop/mobile usage rows.
- `apps/console-ui/src/pages/AdminPages.tsx`: desktop/mobile account hierarchy and existing actions.
- `apps/console-ui/src/styles.css`: stable responsive table/card layout.
- `tools/console-browser-qa.ts`: fake authoritative fixtures, interactions, screenshots, and overflow checks.
- `tests/**`: RED/GREEN contract, Go, source-level UI, and browser evidence.
- `packages/contracts/*.json`, `docs/invariants.md`, `docs/product/console-display-contract-v1.md`: synchronized machine and human truth.

### Task 1: Freeze The Expanded Truth

**Files:**
- Modify: `tests/contracts/source-truth-contract.test.ts`
- Modify: `tests/contracts/launch-architecture-freeze.test.ts`
- Modify: `tests/ui/react-console-surface.test.ts`
- Modify: `packages/contracts/opl-cloud-launch-freeze-contract.json`
- Modify: `packages/contracts/opl-cloud-console-source-truth-contract.json`
- Modify: `packages/contracts/opl-cloud-console-ui-contract.json`
- Modify: `docs/invariants.md`
- Modify: `docs/product/console-display-contract-v1.md`

- [x] **Step 1: Write failing machine-contract tests**

Require these exact upstream and customer allowlists:

```ts
assert.deepEqual(freeze.gateway.usageListFields, [
  "user_id", "api_key_id", "request_id", "created_at", "model", "inbound_endpoint", "request_type",
  "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "actual_cost",
  "duration_ms", "first_token_ms"
]);
assert.deepEqual(gateway.usage.itemFields, [
  "apiKeyId", "requestId", "createdAt", "model", "inboundEndpoint", "requestType",
  "inputTokens", "outputTokens", "cacheCreationTokens", "cacheReadTokens", "actualCostUsdMicros",
  "durationMs", "firstTokenMs"
]);
assert.equal(gateway.usage.latencyEncoding, "nullable_non_negative_integer_ms");
```

Require the UI contract to expose:

```ts
assert.deepEqual(contract.usageRecordPresentation, {
  desktopColumns: ["model_endpoint", "tokens", "actual_cost", "latency", "time", "request_id"],
  tokenFields: ["inputTokens", "outputTokens", "cacheReadTokens", "cacheCreationTokens"],
  latencyFields: ["firstTokenMs", "durationMs"],
  missingLatency: "dash_never_zero_or_derived"
});
assert.deepEqual(contract.operatorAccountPresentation, {
  desktopColumns: ["user", "account_mapping", "balance", "api_cost", "resources", "status", "actions"],
  nestedSourceDiagnostics: "account_detail_only",
  browserSearchOrSort: false,
  mobile: "compact_cards_same_fact_order"
});
```

- [x] **Step 2: Verify RED**

Run `node --test tests/contracts/source-truth-contract.test.ts tests/contracts/launch-architecture-freeze.test.ts tests/ui/react-console-surface.test.ts`.

Expected: FAIL because the latency and presentation contract fields are absent.

- [x] **Step 3: Update machine and human contracts**

Add upstream `duration_ms` / `first_token_ms`, customer `durationMs` / `firstTokenMs`, `latencyEncoding`, and the two exact UI objects above. Rewrite C-API-02 to require model/endpoint, Token breakdown, actual cost, first-token/total latency, time, and request ID. Rewrite A-ACC-01 to require the seven-column desktop hierarchy and move nested source diagnostics into A-ACC-03. State in `docs/invariants.md` that latency is live, nullable, non-negative, unpersisted, and shown as `-` when missing.

- [x] **Step 4: Verify GREEN**

Run the Step 2 command again. Expected: PASS.

### Task 2: Carry Nullable Latency Through Control Plane

**Files:**
- Modify: `services/control-plane/internal/clients/sub2api_test.go`
- Modify: `services/control-plane/internal/clients/sub2api.go`
- Modify: `services/control-plane/internal/server/source_truth_gateway_test.go`
- Modify: `services/control-plane/internal/server/routes_gateway.go`

- [x] **Step 1: Write failing decoder tests**

Add `"duration_ms":987,"first_token_ms":123` to the accepted fixture and assert:

```go
if row.DurationMS == nil || *row.DurationMS != 987 || row.FirstTokenMS == nil || *row.FirstTokenMS != 123 {
	t.Fatalf("usage latency = duration:%v first-token:%v", row.DurationMS, row.FirstTokenMS)
}
```

Add table cases proving null/missing values succeed as nil while negative/fractional values fail closed.

- [x] **Step 2: Verify decoder RED**

Run `cd services/control-plane && go test ./internal/clients -run 'TestSub2APIUsageList' -count=1`.

Expected: FAIL because the record has no latency fields.

- [x] **Step 3: Implement the strict decoder**

Add these fields to both the internal record and upstream row:

```go
DurationMS   *int64 `json:"duration_ms"`
FirstTokenMS *int64 `json:"first_token_ms"`
```

Reject either value when non-nil and negative, then copy both pointers into `Sub2APIUsageRecord`. Do not add storage or inferred values.

- [x] **Step 4: Verify decoder GREEN**

Run the Step 2 command again. Expected: PASS.

- [x] **Step 5: Write a failing response-projection test**

Give the server fixture one present latency and one nil latency. Assert the exact response item has thirteen keys, `durationMs` is numeric, `firstTokenMs` is JSON null, and no snake_case or raw content fields appear.

- [x] **Step 6: Verify projection RED**

Run `cd services/control-plane && go test ./internal/server -run 'TestGatewaySourceTruthRoutesUseSessionIdentityAndStrictEnvelopes' -count=1`.

Expected: FAIL because response projection omits latency.

- [x] **Step 7: Project only approved latency**

Extend `writeGatewayUsagePage` with:

```go
"durationMs": item.DurationMS,
"firstTokenMs": item.FirstTokenMS,
```

- [x] **Step 8: Verify backend GREEN**

Run `cd services/control-plane && go test ./internal/clients ./internal/server -run 'Test(Sub2APIUsageList|GatewaySourceTruthRoutesUseSessionIdentityAndStrictEnvelopes|GatewayPerKeyUsage)' -count=1`.

Expected: PASS.

### Task 3: Render The Six-Column Usage Record

**Files:**
- Modify: `tests/ui/customer-console-flow.test.ts`
- Modify: `tests/ui/react-console-surface.test.ts`
- Modify: `apps/console-ui/src/api/dtos.ts`
- Modify: `apps/console-ui/src/pages/CustomerPages.tsx`
- Modify: `apps/console-ui/src/styles.css`

- [x] **Step 1: Write failing React tests**

Require `durationMs: number | null`, `firstTokenMs: number | null`, desktop headers `["模型 / 端点", "Token", "费用", "延迟", "时间", "请求 ID"]`, input/output Token, nonzero-only cache Token rows, both latency labels, and null latency formatting as `-` while preserving an authoritative zero as `0 ms`.

- [x] **Step 2: Verify UI RED**

Run `node --test tests/ui/customer-console-flow.test.ts tests/ui/react-console-surface.test.ts`.

Expected: FAIL because DTO and rows omit Token and latency.

- [x] **Step 3: Implement the DTO and row components**

Add:

```ts
durationMs: number | null;
firstTokenMs: number | null;
```

Use exactly:

```ts
function formatLatency(value: number | null) {
  return value === null ? "-" : `${formatCount(value)} ms`;
}
```

Render stacked model/endpoint, input/output Token, nonzero cache-read/cache-write Token, actual cost, first-token/total latency, time, and request ID. Keep the same facts and order in mobile cards.

- [x] **Step 4: Stabilize dimensions**

Give the desktop table a fixed minimum width, allow endpoints and request IDs to wrap, and define a four-fact mobile grid without horizontal scrolling or overlap.

- [x] **Step 5: Verify UI GREEN**

Run `node --test tests/ui/customer-console-flow.test.ts tests/ui/react-console-surface.test.ts`, `npm run typecheck`, and `npm run lint`.

Expected: all commands exit 0.

### Task 4: Freeze The Seven-Column Admin Account Table

**Files:**
- Modify: `tests/ui/operator-console-flow.test.ts`
- Modify: `tests/ui/react-console-surface.test.ts`
- Modify: `apps/console-ui/src/pages/AdminPages.tsx`
- Modify: `apps/console-ui/src/styles.css`

- [x] **Step 1: Write failing account-layout tests**

Require headers `["用户", "账户映射", "余额", "API 费用", "资源", "状态", "操作"]`, mapping labels for OPL Account / Console User / Sub2API User, today/cumulative API cost, Key/Workspace in one resource cell, and `AccountSourceSummary` only in account detail. Assert no search/sort control exists.

- [x] **Step 2: Verify layout RED**

Run `node --test tests/ui/operator-console-flow.test.ts tests/ui/react-console-surface.test.ts`.

Expected: FAIL because the current desktop table has ten columns, inline diagnostics, and CSS hides the table.

- [x] **Step 3: Implement table and mobile hierarchy**

Compose only from existing `OperatorAccountDTO`: user=email/role; mapping=OPL Account/Console User/live Sub2API User; balance=USD/status; API cost=today/cumulative; resources=Key/Workspace; status=existing badge; actions=existing detail/wallet/disable. Move `AccountSourceSummary` into `AccountDetailModal` and preserve all source states there.

- [x] **Step 4: Make breakpoints explicit**

Show `.operator-account-table` above 820px and `.operator-account-mobile-list` at 820px or below. Remove current desktop card-grid overrides. Keep stable action targets and wrapping identifiers.

- [x] **Step 5: Verify layout GREEN**

Run `node --test tests/ui/operator-console-flow.test.ts tests/ui/react-console-surface.test.ts`, `npm run typecheck`, and `npm run lint`.

Expected: all commands exit 0.

### Task 5: Browser Evidence

**Files:**
- Modify: `tools/console-browser-qa.ts`
- Create: `output/browser-qa/sub2api-alignment/fixture-api-usage-desktop.png`
- Create: `output/browser-qa/sub2api-alignment/fixture-api-usage-mobile.png`
- Create: `output/browser-qa/sub2api-alignment/fixture-admin-accounts-desktop.png`
- Create: `output/browser-qa/sub2api-alignment/fixture-admin-accounts-mobile.png`

- [x] **Step 1: Write failing browser assertions**

Add present and null latency fixtures. Require six usage headers, Token/cost/latency/time facts on both viewports, and `-` for null latency. Require desktop accounts to use `.operator-account-table:visible`, mobile accounts to use `.operator-account-mobile-card:visible`, seven desktop headers, and existing account commands.

- [x] **Step 2: Verify browser RED**

Run `OPL_CONSOLE_QA_SCREENSHOT_DIR=output/browser-qa/sub2api-alignment node tools/console-browser-qa.ts --network=fake-only`.

Expected: FAIL until fixture assertions match the new UI.

- [x] **Step 3: Align fixtures and verify GREEN**

Keep fake-only networking, source-state coverage, account actions, pagination, retry behavior, and zero external-request/error assertions. Run the Step 2 command again and expect exit 0.

- [x] **Step 4: Inspect screenshots**

Inspect the four requested desktop/mobile images for overflow, clipped IDs, readable density, stable fact order, and non-overlapping controls.

### Task 6: Full Verification

**Files:**
- Verify only; do not modify frozen ImageGen PNGs.

- [x] **Step 1: Format and validate**

Run:

```bash
gofmt -w services/control-plane/internal/clients/sub2api.go services/control-plane/internal/clients/sub2api_test.go services/control-plane/internal/server/routes_gateway.go services/control-plane/internal/server/source_truth_gateway_test.go
git diff --check
jq empty packages/contracts/opl-cloud-launch-freeze-contract.json packages/contracts/opl-cloud-console-source-truth-contract.json packages/contracts/opl-cloud-console-ui-contract.json
```

Expected: all commands exit 0.

- [x] **Step 2: Run all gates**

Run `npm test`, `npm run typecheck`, `npm run lint`, `npm run build`, and `cd services/control-plane && go test ./...`.

Expected: all commands exit 0 with zero failures.

- [x] **Step 3: Verify assets and scope**

Run `sha256sum` on both frozen PNGs, followed by `git status --short`, `git diff --stat`, and `git diff --check`.

Expected hashes:

```text
897f657539d2ccd8e10df365e5108094bee3a68ffd1c349190f692501657b4b6
9b75ba8b01cda552fcb7d44bc774797f22697f5fd4a0c067e885bc4895b738b5
```

Confirm the diff remains isolated to this worktree and no commit or merge occurred.
