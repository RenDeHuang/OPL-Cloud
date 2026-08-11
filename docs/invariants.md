# OPL Cloud Durable Invariants

This document owns long-lived safety, integrity, authority, and evidence rules.
It does not own current delivery status, workflow steps, source layout, visual
direction, rollout candidates, or open work. Current facts belong to
`docs/status.md`, open gaps to `docs/roadmap.md`, and executable detail to source,
schemas, workflows, and focused tests.

## Authority And Change Direction

- `one-person-lab-cloud` is the single product and reusable implementation
  repository for Console, Control Plane, Fabric, Ledger, and Workspace delivery.
  `opl-cloud` is an internal artifact and service identifier, not a second owner.
- Product concept and target architecture govern lower layers. A target change
  must reconcile affected implementation docs, module docs, contracts, status,
  and roadmap without creating a second current truth.
- A document or machine contract cannot prove implementation, deployment, or
  production state. Claims must be supported by the corresponding source,
  schema, tests, runtime, or production readback.
- Machine contracts protect deterministic cross-module, public-interface,
  security, integrity, permission, and irreversible-side-effect boundaries.
  They do not own UI taste, internal tuning, file layout, workflow command
  sequences, current progress, or pending evidence.

## Module And Data Ownership

- Console calls only Control Plane product APIs. It owns presentation and
  interaction, never persistence, provider mutation, billing authority, or
  downstream service truth.
- Control Plane owns Sessions, account policy, Workspace orchestration,
  entitlements, settlement coordination, and customer-safe DTOs.
- Fabric is the only provider-resource and Kubernetes writer. Provider-specific
  behavior stays behind the Fabric provider adapter.
- Ledger owns append-only receipts, evidence, review, reconciliation, and
  continuation references. It never owns or changes spendable balance.
- Sub2API is the only authority for customer identity credentials, spendable USD
  balance, API keys, model routing, and request usage. Cloud must not create a
  second wallet, Key store, Usage store, or Gateway service.
- Control Plane, Fabric, and Ledger remain separate processes and PostgreSQL
  schema owners. Cross-service integration uses typed public HTTP contracts;
  no service imports another service's internal source or reads another
  service's tables.
- Workspace file bodies live only on their owned storage volume. Platform
  PostgreSQL and Ledger may store identity, operation, reference, and evidence
  facts, but never Workspace file contents.

## Identity And Tenant Isolation

- One Console User maps to one OPL Account and one Sub2API User/Wallet. The
  signed-in Session determines the account scope; browser-supplied account or
  downstream user identifiers cannot override it.
- One Account may own zero or more independent Workspaces. Every Workspace has
  its own stable identity, resources, credentials, entitlement period, and
  receipts. There is no account-singleton Workspace invariant or fixed product
  count limit.
- Operator metadata access does not grant owner access to another account's API
  Key, Runtime password, Workspace credential, or private resource details.
- Organization and Membership compatibility rows do not authorize sharing and
  do not become customer-facing identity truth.
- Missing, ambiguous, inconsistent, or unavailable owner identity readback
  fails closed for protected access or mutation. It must never be replaced by a
  stale local projection.

## Secrets And Browser Boundary

- Passwords, raw API keys, tokens, provider credentials, approval payloads, and
  raw downstream responses never enter URLs, logs, audit payloads, Ledger,
  browser storage, or non-secret artifacts.
- Runtime passwords and owned API Keys are masked by default, revealed only to
  the authorized owner, returned with `private, no-store`, and kept only for the
  bounded interaction that requested them.
- Kubernetes Secret is the only authorized persistence point for a Workspace
  Gateway Key. Runtime receives only the scoped Secret reference.
- `OPL_SUB2API_BASE_URL` and Sub2API management credentials remain server-only.
  The browser never calls, embeds, redirects to, or scrapes the management
  surface. The public `/v1` model endpoint may be presented according to the
  current Console UX without weakening that management boundary.
- Every customer-facing downstream projection uses an explicit allowlist and
  excludes raw admin DTOs, credentials, prompts, response bodies, and provider
  secrets.

## Money And Settlement

- Customer prices and balance mutations use exact integer USD micros. Provider
  costs do not derive customer charges.
- Each Workspace purchase or renewal has at most one confirmed customer debit
  for the total period price. Compute and storage are fulfillment, never
  separate customer charges.
- Debit, refund, provider mutation, claim, activation, renewal, Secret write,
  and receipt use stable operation-scoped idempotency identities.
- A confirmed provider result proving that no billable resource exists after a
  debit permits exactly one idempotent refund. A partial, conflicting, or
  unknown provider result enters manual review without refund or a second
  purchase.
- A receipt failure after activation retries only the receipt. It never repeats
  debit, refund, provider purchase, Secret write, activation, or renewal.
- Concurrent legitimate model Usage may change the live wallet. A confirmed
  debit is proved by the unique matching Sub2API mutation history, not by
  assuming an exact before/after balance delta.
- Wallet writes remain serialized by the Control Plane's owning boundary. A
  second lock service or multi-replica wallet writer requires an explicit
  architecture and contract change.

## Launch And Recovery

- Read-only identity, availability, capacity, price, and balance preflight
  completes before the first debit or provider write. A failed preflight has
  zero customer charge and zero provider mutation.
- A Workspace launch is one durable, resumable Control Plane operation. Replay
  continues the original identities and remaining attempt budgets; it never
  creates a second launch, debit, Key, CVM, CBS, Runtime, or receipt.
- External writes are reserved before execution. A reserved or unknown result
  is reconciled through authoritative readback and is never blindly reissued.
- Each continuation stage has a bounded write budget. Exhausted or unknown
  outcomes enter manual review and cannot reset their budget after restart.
- Recovery is an authorization path for the original launch, not a second
  business state machine. Dedicated `workspace.launch.v2` review recovery uses
  the Console flow `diagnose -> view persisted Recovery Plan -> validate ->
  confirm continue`.
- Recovery resource identity comes only from Control Plane, Fabric, provider,
  Kubernetes, Sub2API, and Ledger readback. Console or workflow input cannot
  supply CVM, Node, storage, Secret, image, or approval authority.
- A failed recovery may create a successor only after server-authoritative
  confirmed-zero evidence, or after Fabric evidence proves the original compute
  mutation ledger is absent or observed with complete confirmed-zero evidence.
  Missing, incomplete, positive, or unknown evidence cannot be treated as zero.
- Fenced leases and compare-and-swap persistence ensure one winner. A stale
  worker cannot finalize after losing its lease.

## Provider And Resource Safety

- Customer and verification CVM/CBS procurement uses `PREPAID`, one month, and
  `NOTIFY_AND_MANUAL_RENEW`. `POSTPAID_BY_HOUR` is forbidden.
- Provider capacity and price checks are read-only. They do not buy, reserve,
  renew, or delete resources.
- Real provider mutation requires an explicit production authorization bound to
  the exact release, caller identity, target, allowed writes, and expiry.
- Provider and Kubernetes mutations use authoritative identity readback and
  exact mutation bounds. Ambiguous identity, ownership conflict, permission
  failure, or unknown result fails closed before another write.
- System resources designated by the deployed instance are protected from
  customer allocation, provider mutation, Kubernetes mutation, and cleanup.
  Their identifiers belong to deployment/instance authority, not this document.
- A launch claims only resources created or authoritatively bound to that
  Workspace. It cannot reuse an old, idle, orphaned, unregistered, or
  differently owned machine or volume.
- Once the original storage identity is confirmed, replacement storage creation
  is forbidden. Recovery converges the original identity or remains in manual
  review.
- Unpaid expiry denies Workspace access and performs zero Fabric or provider
  resource mutation. Provider expiry policy owns eventual reclamation.

## Console Experience Outcomes

- Public and login entry remains immediately usable; session checks may enrich
  or redirect but do not block the first interactive screen.
- The authenticated Console presents authoritative account, wallet, Workspace,
  usage, receipt, and actionable failure facts without fabricating unavailable
  values or copying downstream truth.
- Independent data sources load and fail independently. One unavailable source
  cannot erase valid facts from another or hold the entire Console indefinitely.
- `empty` means a successful authoritative read with zero rows;
  `unavailable` means authority could not be read and contains no invented
  fallback data.
- The Console must be professional, understandable, responsive, keyboard
  accessible, and safe for sensitive information. Colors, gradients, exact
  dimensions, navigation count, component library, framework, model choice, and
  asset hashes are implementation decisions, not invariants.

## Deployment And Release Safety

- Production mutation runs only through approved GitHub Actions environments and
  authorized runners. Local development cannot directly access production
  private endpoints, clusters, databases, or services.
- Production source and images are immutable and bound to exact merged commits
  and digest readback. Branch names, mutable image tags, placeholders, and local
  timestamps are not release evidence.
- Secrets remain in approved secret stores and temporary protected files. They
  never appear in manifests, command arguments, logs, caches, or artifacts.
- Ordinary rollout is read-only with respect to customer billing and provider
  resources. A real customer or provider mutation requires a separate explicit
  approval and exact mutation budget.
- Deployment captures authoritative diagnostics before rollback. Rollback
  restores the prior approved images and configuration without inventing a new
  product or billing state.

## Evidence Levels

- `code-complete` requires the current implementation revision and its complete
  local machine-enforced gates, including required database suites and zero
  skipped tests.
- `pilot-ready` additionally requires separately approved real Gateway,
  Runtime, provider, billing, and browser evidence for that exact revision.
- `production-proven` additionally requires the same immutable revision to be
  deployed and read back with the required production evidence.
- A lower evidence level never implies a higher one. Documentation, contracts,
  fake tests, screenshots, CI, or a rendered artifact prove only their own
  layer.
