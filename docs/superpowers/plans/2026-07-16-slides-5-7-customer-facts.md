# Slides 5 and 7 Customer Facts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Repository policy forbids subagents for this work.

**Goal:** Expose complete tenant billing history plus Sub2API request usage/stats and compute an exception-only reconciliation report without copying authoritative facts.

**Architecture:** Extend the existing Ledger and Sub2API HTTP clients with bounded, typed read APIs. Control Plane resolves the signed-in account and exactly one active `opl-workspace` Key, validates every returned identity, and emits strict customer DTOs. The existing operator reconciliation endpoint becomes server-computed and appends its report to Ledger; it never adjusts money or provider resources.

**Tech Stack:** Go 1.22, `net/http`, `encoding/json` with `json.Number`, existing Ledger/Sub2API APIs, Go tests.

---

## File Map

- Modify `services/control-plane/internal/clients/ledger.go`: receipt list query/page types and HTTP method.
- Modify `services/control-plane/internal/clients/ledger_test.go`: query, bounds, body limit, and decode tests.
- Modify `services/control-plane/internal/clients/sub2api.go`: request usage, stats, and balance-history adapters.
- Modify `services/control-plane/internal/clients/sub2api_test.go`: scope, pagination, decimal, identity, and leakage tests.
- Modify `services/control-plane/internal/controlplane/service.go`: typed pass-through methods and one-Key scope.
- Modify `services/control-plane/internal/server/routes_billing.go`: billing list/detail projection and reconciliation.
- Modify `services/control-plane/internal/server/routes_gateway.go`: request list and stats routes.
- Modify `services/control-plane/internal/server/billing_projection.go`: reconciliation report builder.
- Create `services/control-plane/internal/server/customer_facts_test.go`: tenant, projection, unavailable, and reconciliation tests.
- Modify shared fakes only where interfaces require methods.
- Modify `packages/contracts/opl-cloud-service-boundary-contract.json`, `packages/contracts/opl-cloud-launch-freeze-contract.json`, and `docs/invariants.md`.

Do not modify Ledger storage/schema, Sub2API source/deployment, any UI file,
Workspace launch code, Runtime credential routes, Fabric provider mutations, or
production workflows.

### Task 1: Establish the clean baseline

- [ ] **Step 1: Verify isolation and run focused tests**

Run:

```bash
git status --short --branch
go test ./services/control-plane/internal/clients ./services/control-plane/internal/controlplane ./services/control-plane/internal/server ./services/ledger/...
```

Expected: branch `feat/slides-5-7-customer-facts`, clean worktree, PASS.

### Task 2: Add a bounded Ledger receipt list client

- [ ] **Step 1: Write failing client tests**

Cover this request exactly:

```text
GET /ledger/receipts?accountId=acct-alpha&cursor=opaque&limit=50
Authorization: Bearer <internal token>
```

Use this fixture:

```json
{
  "receipts": [{"receiptId":"receipt-1","type":"billing.resource_purchased.v1","status":"completed","accountId":"acct-alpha","workspaceId":"ws-alpha","createdAt":"2026-07-16T00:00:00Z"}],
  "nextCursor": "next",
  "hasMore": true
}
```

Also assert invalid limit is rejected before HTTP and a response over 1 MiB is
rejected without embedding its body in the error.

- [ ] **Step 2: Confirm the method is absent**

Run:

```bash
go test ./services/control-plane/internal/clients -run TestLedgerReceiptList -count=1
```

Expected: compile FAIL until the method exists.

- [ ] **Step 3: Implement the query and page**

Add:

```go
type ReceiptQuery struct {
	AccountID string
	Cursor    string
	Limit     int
}

type ReceiptPage struct {
	Receipts   []Receipt `json:"receipts"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}
```

Add `ListReceipts(context.Context, ReceiptQuery) (ReceiptPage, error)` to
`LedgerClient`. Build the query with `url.Values`; allow limits 1-100 and default
50. Reuse authorization and a bounded JSON decoder. Do not add a cache.

- [ ] **Step 4: Test and commit**

Run:

```bash
go test ./services/control-plane/internal/clients -run 'Ledger.*Receipt' -count=1
git add services/control-plane/internal/clients/ledger.go services/control-plane/internal/clients/ledger_test.go
git commit -m "feat(control-plane): read paginated ledger receipts"
```

Expected: PASS.

### Task 3: Expose tenant-safe billing history

- [ ] **Step 1: Write failing list/detail tests**

Assert `/api/billing/receipts?limit=50` always sends the session account ID;
another account's receipt produces `502 billing_receipt_identity_mismatch`;
non-`billing.` types are omitted; list/detail omit `plan`, `execution`,
`environment`, raw provider payload, Sub2API response, credential, and secret
fields; pagination survives; and a Ledger failure affects this endpoint only.

- [ ] **Step 2: Add the safe projector**

Use this allowlist for both list and detail:

```go
map[string]any{
	"receiptId": receipt.ReceiptID,
	"type": receipt.Type,
	"status": receipt.Status,
	"workspaceId": receipt.WorkspaceID,
	"createdAt": receipt.CreatedAt,
	"resourceType": stringValue(receipt.Cost["resourceType"]),
	"resourceId": stringValue(receipt.Cost["resourceId"]),
	"pricingVersion": stringValue(receipt.Cost["pricingVersion"]),
	"monthlyPriceCnyCents": requiredInteger(receipt.Cost, "monthlyPriceCnyCents"),
	"chargeUsdMicros": requiredInteger(receipt.Cost, "chargeUsdMicros"),
	"periodStart": stringValue(receipt.Cost["periodStart"]),
	"paidThrough": stringValue(receipt.Cost["paidThrough"]),
}
```

Malformed required integer facts make the source unavailable; never substitute
zero.

- [ ] **Step 3: Test and commit**

Run:

```bash
go test ./services/control-plane/internal/server -run 'BillingReceipt.*(List|Detail|Tenant|Projection)' -count=1
git add services/control-plane/internal/controlplane/service.go services/control-plane/internal/server/routes_billing.go services/control-plane/internal/server/customer_facts_test.go services/control-plane/internal/server/server_test.go
git commit -m "feat(control-plane): project customer billing history"
```

Expected: PASS.

### Task 4: Add strict Sub2API usage and stats adapters

- [ ] **Step 1: Write failing usage-list tests**

The client must call:

```text
GET /api/v1/admin/usage?user_id=41&api_key_id=9&page=1&page_size=50&sort_by=created_at&sort_order=desc
```

Fixture one row with all contracted fields plus forbidden nested `user`,
`api_key`, IP, user agent, prompts, and response content. Assert only these typed
fields survive: `user_id`, `api_key_id`, `request_id`, `created_at`, `model`,
`inbound_endpoint`, `request_type`, the four Token fields, and `actual_cost`.
Assert a cross-user or cross-Key row fails the whole response.

- [ ] **Step 2: Write failing stats and money tests**

Call `/api/v1/admin/usage/stats` with both identities and expose only
`total_requests`, `total_input_tokens`, `total_output_tokens`, `total_tokens`,
and `total_actual_cost`. Test exact conversion for `0`, `0.000001`, and
`12.345678`; reject negative tokens, missing cost, overflow, and decimals not
representable as integer micros. Do not round silently.

- [ ] **Step 3: Implement concrete DTOs and optional interface**

```go
type Sub2APIUsageClient interface {
	Usage(context.Context, Sub2APIUsageQuery) (Sub2APIUsagePage, error)
	UsageStats(context.Context, Sub2APIUsageStatsQuery) (Sub2APIUsageStats, error)
	BalanceHistory(context.Context, int64) ([]Sub2APIBalanceHistoryEntry, error)
}
```

Use `json.Number` and existing `decimalUSDMicros`. Bound usage pages to 100 rows,
page to a positive safe integer, balance-history pages to 10 x 1000, and each
response to the existing 1 MiB maximum.

- [ ] **Step 4: Implement balance-history validation**

Call:

```text
GET /api/v1/admin/users/{id}/balance-history?page=N&page_size=1000&type=balance
```

Decode only `code`, `type`, `value`, `status`, `used_by`, `used_at`, and
`created_at`. A used entry with another `used_by` fails closed. Never expose
`notes`.

- [ ] **Step 5: Test and commit**

Run:

```bash
go test ./services/control-plane/internal/clients -run 'Sub2API.*(Usage|BalanceHistory)' -count=1
git add services/control-plane/internal/clients/sub2api.go services/control-plane/internal/clients/sub2api_test.go
git commit -m "feat(control-plane): read scoped sub2api usage facts"
```

Expected: PASS.

### Task 5: Expose usage and aggregate stats

- [ ] **Step 1: Write failing tenant route tests**

For both routes, assert Control Plane selects Key ID 9 for mapped user 41 and
passes both identities:

```text
GET /api/gateway/usage?page=1&pageSize=50
GET /api/gateway/usage/stats?period=month
```

An account member may view non-secret usage. Query parameters cannot override
account or Key identity. Missing/ambiguous Key and upstream failure return an
explicit unavailable response with no numeric zeros.

- [ ] **Step 2: Add service methods**

`GatewayUsage` and `GatewayUsageStats` call `Sub2APIWorkspaceKey` first, then the
optional usage interface. A missing capability returns
`sub2api_usage_unavailable`; do not fall back to Key window counters.

- [ ] **Step 3: Add strict route DTOs**

Return request rows shaped as:

```json
{
  "requestId": "req-1",
  "createdAt": "2026-07-16T00:00:00Z",
  "model": "gpt-5",
  "inboundEndpoint": "/v1/responses",
  "requestType": "sync",
  "inputTokens": 10,
  "outputTokens": 20,
  "cacheCreationTokens": 0,
  "cacheReadTokens": 5,
  "actualCostUsdMicros": 1234
}
```

Stats use corresponding `total*` fields and `totalActualCostUsdMicros`.

- [ ] **Step 4: Test and commit**

Run:

```bash
go test ./services/control-plane/internal/server -run 'Gateway.*(Usage|Stats|Tenant|Unavailable)' -count=1
git add services/control-plane/internal/controlplane/service.go services/control-plane/internal/server/routes_gateway.go services/control-plane/internal/server/customer_facts_test.go services/control-plane/internal/server/server_test.go
git commit -m "feat(control-plane): project gateway request usage"
```

Expected: PASS.

### Task 6: Compute reconciliation from authoritative facts

- [ ] **Step 1: Write complete-match and mismatch tests**

Seed active compute/storage rows with deterministic charge codes, provider IDs,
and receipt IDs. A matching Sub2API history, Fabric operation, and Ledger page
must yield `status=ok`. Independently remove/change each fact; each case must
yield `mismatch`, block a new purchase, and perform no refund, debit, provider
mutation, or receipt correction.

- [ ] **Step 2: Implement the exception-only report**

```go
report := map[string]any{
	"id": "reconciliation-" + stableID(idempotencyKey)[:18],
	"status": status,
	"checkedAt": now.UTC().Format(time.RFC3339),
	"counts": map[string]any{
		"billingOperations": checked,
		"matched": matched,
		"exceptions": len(exceptions),
	},
	"exceptions": exceptions,
}
```

Exceptions contain stable resource references and machine codes only. Treat any
source unavailable as an exception; exclude raw bodies, notes, account emails,
balances, credentials, and provider error text.

- [ ] **Step 3: Replace caller-supplied reports**

Keep `POST /api/billing/reconciliation`, require operator auth, `confirm=true`,
and `Idempotency-Key`, and reject a caller-supplied `report`. Gather facts on the
server and pass the computed report to existing `RecordReconciliation`.

- [ ] **Step 4: Test and commit**

Run:

```bash
go test ./services/control-plane/internal/server -run 'BillingReconciliation.*(Match|Mismatch|Unavailable|NoMutation)' -count=1
git add services/control-plane/internal/server/routes_billing.go services/control-plane/internal/server/billing_projection.go services/control-plane/internal/server/customer_facts_test.go services/control-plane/internal/server/server_test.go
git commit -m "feat(control-plane): reconcile authoritative billing facts"
```

Expected: PASS.

### Task 7: Update contracts and verify the lane

- [ ] **Step 1: Update current-state contracts**

Record Ledger list, Sub2API usage/stats/balance-history reads, strict DTOs, and
server-computed reconciliation. Keep live model request, deployed receipt, and
production reconciliation evidence pending.

- [ ] **Step 2: Format and test**

Run:

```bash
gofmt -w services/control-plane/internal/clients/ledger.go services/control-plane/internal/clients/ledger_test.go services/control-plane/internal/clients/sub2api.go services/control-plane/internal/clients/sub2api_test.go services/control-plane/internal/controlplane/service.go services/control-plane/internal/server/routes_billing.go services/control-plane/internal/server/routes_gateway.go services/control-plane/internal/server/billing_projection.go services/control-plane/internal/server/customer_facts_test.go
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
npm test
git diff --check integration/pilot-launch-b...HEAD
```

Expected: PASS and no UI, launch worker, Runtime credential, Fabric provider, or
deployment files in the diff.

- [ ] **Step 3: Commit contracts and rebase**

Run:

```bash
git add docs/invariants.md packages/contracts/opl-cloud-service-boundary-contract.json packages/contracts/opl-cloud-launch-freeze-contract.json
git commit -m "docs: contract customer billing and usage facts"
git rebase integration/pilot-launch-b
(cd services/internal/postgresmigrate && go test ./...)
(cd services/ledger && go test ./...)
(cd services/fabric && go test ./...)
(cd services/control-plane && go test ./...)
```

Expected: PASS. The root integration worktree owns the merge.
