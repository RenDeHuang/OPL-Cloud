# OPL Gateway Sub2API Console Integration Design

## Status

Approved direction on 2026-07-11. This document defines the phased delivery design for using the existing Sub2API deployment at `https://gflabtoken.cn` as the private OPL Gateway backend while keeping its server-side update lifecycle independent from OPL Console.

## Goal

Deliver one OPL Cloud customer experience in which a user:

1. Signs in through the OPL identity entry.
2. Opens Gateway from OPL Console without creating or remembering a separate Sub2API password.
3. Uses the existing Sub2API user portal at `https://gflabtoken.cn` to create and manage API keys.
4. Uses a selected key in local Codex and OPL Workspace.
5. Sees Gateway usage and OPL wallet charges in Console.

Sub2API remains independently deployed and updated on its current server. Console integrates through HTTP APIs only.

## Product Boundary

### OPL Console And Control Plane Own

- OPL account and organization identity.
- The customer entry to Gateway.
- Mapping an OPL account to a Sub2API user.
- Gateway summaries and key projections shown in Console.
- Selecting a key for an OPL Workspace.
- OPL wallet holds, CNY charges, receipts, and reconciliation.
- Compatibility checks against the currently deployed Sub2API version.

### Sub2API Owns

- API key creation, update, deletion, quota, expiry, and rate limits.
- Model routing and upstream account selection.
- Request authentication and execution.
- Raw request usage, token counts, and `actual_cost` facts.
- Its independently managed PostgreSQL and Redis data.
- Its server update and database migration lifecycle.

### Fabric Owns

- Writing the selected Gateway key into the Workspace Kubernetes Secret.
- Mounting the key at `/run/secrets/gateway_api_key`.
- Setting `OPL_GATEWAY_API_KEY_FILE=/run/secrets/gateway_api_key`.
- Updating the Workspace Secret and runtime when the selected key changes.

## Non-Goals

- Do not copy or reimplement the Sub2API key editor in Console.
- Do not embed the Sub2API UI in an iframe.
- Do not read or write the Sub2API database directly.
- Do not proxy model traffic through OPL Control Plane.
- Do not synchronize or reuse OPL Console passwords in Sub2API.
- Do not let Console control which Sub2API version is installed on the Gateway server.
- Do not expose Sub2API upstream accounts, channels, credentials, or routing internals to customers.

## Domain And Access Model

`https://gflabtoken.cn` remains the canonical Gateway origin.

Public customer paths:

- Sub2API user portal at the root origin.
- OIDC authentication endpoints under `/api/v1/auth/*`.
- Authenticated user key and usage APIs used by the Sub2API portal.
- Model data-plane endpoints such as `/v1/models`, `/v1/responses`, and compatible provider routes.

Restricted management paths:

- `/api/v1/admin/*` is reachable only from OPL Control Plane and approved operator networks.
- The dedicated Control Plane admin credential is stored as a deployment secret and is not a human administrator credential.

The Caddy boundary must preserve streaming behavior for model routes and enforce the management-path restriction without modifying Sub2API.

## Identity Design

A standards-compliant shared OIDC issuer is the credential authority for OPL Console and Sub2API. The stable identity key is `(issuer, subject)`, not email.

For an existing pilot account, verified email may be used once to link the OIDC subject after an authenticated Console session or administrator approval. After linking, email changes do not change the identity mapping.

The Control Plane provisions the Sub2API user through the admin API with:

- the same verified email and display name;
- role `user`;
- a generated random internal password that is discarded after creation;
- the OIDC identity bound through `POST /api/v1/admin/users/:id/auth-identities`.

The user never receives or uses the generated Sub2API password. Clicking Gateway in Console starts or resumes the OIDC flow and lands the user in the Sub2API portal without a second product account.

## Control Plane Integration

The Control Plane uses a concrete Sub2API HTTP client. It does not expose Sub2API responses directly to the browser.

Required service and identity calls:

- `POST /api/v1/auth/login`
- `POST /api/v1/auth/refresh`
- `GET /health`
- `GET /api/v1/admin/system/version`
- `GET /api/v1/admin/users?search=<verified-email>`
- `POST /api/v1/admin/users`
- `GET /api/v1/admin/users/:id`
- `PUT /api/v1/admin/users/:id`
- `POST /api/v1/admin/users/:id/auth-identities`
- `POST /api/v1/admin/users/:id/balance`

Required key and usage calls:

- `GET /api/v1/admin/users/:id/api-keys`
- `GET /api/v1/admin/users/:id/usage`
- `GET /api/v1/admin/usage`
- `GET /api/v1/admin/usage/stats`
- `POST /api/v1/admin/dashboard/api-keys-usage` when batch summaries are needed

The current Sub2API admin API can list a user's keys but cannot create or delete one on behalf of that user. Key mutations therefore remain in the Sub2API user portal in this design. No Sub2API source patch or private fork is required.

## Console User Surface

The Gateway page replaces the current external placeholder with live projections.

Gateway overview:

- online or unavailable status;
- canonical base URL `https://gflabtoken.cn`;
- OPL wallet available balance;
- allocated Gateway budget;
- today's and current month's requests, tokens, and charges;
- last successful usage synchronization.

Key summary:

- name and masked key;
- Sub2API key ID;
- active, disabled, expired, or quota-exhausted status;
- created, last-used, and expiry timestamps;
- quota, quota used, and rate-limit windows;
- linked Workspace when the key is assigned to one;
- action to open the Sub2API key portal through SSO;
- action to select an existing active key for a Workspace.

The full key is masked by default. A reveal action requires a recent Console authentication check, fetches the current value from Sub2API, returns it with `Cache-Control: no-store`, and does not persist it in the Control Plane database or logs.

Usage and cost:

- request time and request ID;
- key name and ID;
- linked Workspace when attribution is defined by the selected key;
- requested model and request type;
- input, output, cache-read, and cache-write tokens;
- Sub2API `actual_cost` in USD;
- the pricing snapshot and final OPL charge in CNY;
- daily, model, key, and Workspace summaries.

## Workspace Key Flow

1. The user creates a key in the Sub2API portal through the SSO entry.
2. Console refreshes the key projection through the admin API.
3. The user selects an active key while creating or updating a Workspace.
4. Control Plane fetches the full key only for the internal provisioning request.
5. Control Plane passes the sensitive value to Fabric over the internal service connection.
6. Fabric writes the value to the Workspace Kubernetes Secret and never includes it in operation facts, logs, receipts, or error payloads.
7. `one-person-lab-app` reads the mounted file through `OPL_GATEWAY_API_KEY_FILE`; the WebUI does not ask the user to enter the key again.

A key may be used by local Codex and a Workspace. Usage attribution is key-based: if one key is linked to a Workspace and also used locally, all usage for that key is attributed to that Workspace. Users who require separate attribution use separate keys.

If the selected key becomes disabled, deleted, expired, or quota exhausted, Console shows the Workspace Gateway binding as unhealthy. Rebinding updates the Secret and rolls the Workspace runtime. Workspace creation that requires model access fails closed when no active key is selected.

## Billing Design

OPL Ledger is the only customer money authority. Sub2API balance is a bounded technical execution credit and is not shown as a second wallet.

The user allocates a CNY Gateway budget from the OPL wallet. Ledger creates a hold before technical credit is added to Sub2API.

For a pricing snapshot:

```text
technical_credit_usd = held_cny / (usd_cny_rate * (1 + gateway_markup))
customer_charge_cny = actual_cost_usd * usd_cny_rate * (1 + gateway_markup)
```

Each pricing snapshot records:

- currency pair and rate;
- Gateway markup;
- effective timestamp and pricing version;
- rounding rule;
- hold and account references.

Control Plane adds technical credit through the idempotent Sub2API balance API. Sub2API decrements that credit as requests run. The usage synchronizer reads usage records with an overlapping time window and deduplicates by Sub2API usage ID.

Each settled Ledger record stores:

- Sub2API usage ID and request ID;
- OPL account, user, key, and Workspace references;
- model and token counts;
- raw `actual_cost` and USD amount;
- pricing snapshot;
- final CNY charge;
- idempotency key and synchronization timestamp.

The synchronizer settles the OPL hold but does not repeatedly overwrite the live Sub2API balance. Reconciliation compares expected remaining technical credit with the Sub2API balance. A material mismatch stops automatic refill, raises an operator alert, and preserves the bounded existing credit.

## Phased Delivery

### Phase 0: Read-Only Contract And Security Boundary

Purpose: prove that Console can safely observe the existing Gateway without changing customer behavior or Sub2API source.

Deliver:

- dedicated Control Plane administrator in Sub2API;
- deployment secrets for the admin session;
- management API network restriction in Caddy;
- Sub2API client for login, refresh, health, version, users, keys, and usage;
- live read-only Gateway status in Console;
- compatibility checks against the deployed Sub2API version;
- removal of plaintext server credentials from repository-adjacent operational files and migration to SSH keys.

Exit gate:

- Console reads health, version, a test user, keys, and usage from `https://gflabtoken.cn`;
- no secret appears in browser state, logs, receipts, or repository files;
- existing Gateway traffic and server update workflow remain unchanged.

### Phase 1: Unified Identity And Gateway Entry

Purpose: remove the second account and password experience while reusing the Sub2API portal.

Deliver:

- shared OIDC issuer configuration for Console and Sub2API;
- `(issuer, subject)` identity storage in Control Plane;
- existing pilot-account linking by verified email with explicit audit evidence;
- lazy Sub2API user provisioning and OIDC identity binding;
- Console Gateway action that opens `https://gflabtoken.cn` through SSO;
- OPL branding and payment-entry suppression in the Sub2API customer portal where supported by configuration.

Exit gate:

- an authenticated Console user enters the Sub2API user portal without a Sub2API password;
- the same OIDC subject maps to exactly one OPL user and one Sub2API user;
- disabled OPL users cannot obtain a new Gateway session.

### Phase 2: Key Projection And Workspace Injection

Purpose: let users manage keys in the existing portal and use them in local Codex and OPL Workspace without WebUI key entry.

Deliver:

- live masked key list in Console;
- guarded key reveal without persistence;
- Codex base URL and model configuration view;
- Workspace key selection and binding record;
- per-Workspace Kubernetes Secret injection through Fabric;
- rotation, revocation, expiry, and unhealthy-binding handling;
- redaction tests covering Control Plane, Fabric operations, Kubernetes manifests, logs, and API errors.

Exit gate:

- a user creates a key through SSO, uses it in local Codex, selects it for a Workspace, and runs a model request from the Workspace without entering the key in WebUI;
- revoking the key blocks further requests and is visible in Console;
- no Control Plane database row contains the full key.

### Phase 3: Unified Gateway Budget And Ledger Settlement

Purpose: make OPL wallet the only customer balance while retaining Sub2API's real-time spending guard.

Deliver:

- Gateway budget allocation and Ledger hold;
- versioned USD/CNY pricing snapshot and Gateway markup;
- idempotent Sub2API technical-credit updates;
- incremental usage synchronization with overlap and deduplication;
- request-level Ledger entries and human-readable receipts;
- Console totals and breakdowns by day, model, key, and linked Workspace;
- reconciliation between Sub2API usage, technical balance, Ledger charges, and wallet holds.

Exit gate:

- duplicate synchronization cannot double-charge;
- insufficient OPL balance cannot create new Gateway credit;
- every CNY Gateway charge traces to one Sub2API usage ID and pricing snapshot;
- reconciliation reports zero unexplained usage or balance drift in the pilot window.

### Phase 4: Independent Upgrade Compatibility And Production Rollout

Purpose: preserve the existing server-first Sub2API update workflow without silently breaking Console integration or billing.

Deliver:

- post-update compatibility command run from the Gateway server;
- continuous Console health and version observation;
- contract checks for admin auth, user lookup, key listing, usage listing, usage stats, and a minimal model request;
- compatibility status and last check in the Console admin surface;
- bounded failure behavior: stop new bindings and automatic credit refill when the management contract is incompatible, while existing data-plane credit remains capped;
- operator alerts, usage-sync lag alerts, backup evidence, and reconciliation runbook;
- controlled pilot followed by wider rollout.

Exit gate:

- updating Sub2API on its server either passes the contract checks with no Console release or produces an explicit incompatibility result;
- an incompatible update cannot create unlimited unbilled exposure;
- rollback and database-backup procedures are exercised with recorded evidence.

## Failure Handling

- Gateway unavailable: Console shows stale data with the last successful synchronization time and disables mutations.
- Admin session expired: Control Plane refreshes once, then fails closed and alerts.
- OIDC mapping conflict: no automatic merge; administrator review is required.
- Key fetch or Secret update fails: Workspace binding remains unchanged and the operation records a redacted failure.
- Usage synchronization timeout: retry the overlapping window with the same usage-ID deduplication rule.
- Currency or pricing configuration missing: do not allocate technical credit or settle usage.
- Balance drift: stop refill, preserve the bounded current credit, and require reconciliation.
- Unsupported Sub2API contract: keep read-only cached projections, disable new bindings and credit changes, and alert operators.

## Verification Strategy

Focused checks by phase:

- API contract fixtures for the current deployed Sub2API responses.
- OIDC subject uniqueness, account-linking, disabled-user, and session tests.
- Key masking, reveal no-store, log redaction, and Workspace Secret tests.
- Real local Codex and Workspace model requests through `https://gflabtoken.cn`.
- Usage overlap, duplicate delivery, out-of-order records, and money-rounding tests.
- Wallet hold, insufficient balance, refill, drift, and reconciliation tests.
- A server-side Sub2API update rehearsal followed by the compatibility command and Console verification.

The production acceptance chain is:

```text
Console OIDC login
-> SSO to gflabtoken.cn
-> create API key
-> use key in local Codex
-> select key for Workspace
-> Workspace model request
-> Sub2API usage record
-> Ledger CNY settlement
-> Console usage and receipt
-> Sub2API server update
-> compatibility verification
```
