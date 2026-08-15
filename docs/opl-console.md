# OPL Console

Owner: `one-person-lab-cloud`
Purpose: `console_target_reference`
State: `active_target_reference`
Machine boundary: Human-readable target product definition. It does not prove
a Console implementation, account state, policy decision, metering, billing,
release, or production readiness.

OPL Console is the user and administrator management surface for OPL Cloud. It
manages account onboarding, the account's Workspace collection, plans, balance
and charge projections, quotas, budgets, approvals, and policy for Cloud-hosted
or explicitly managed resources and Agent Services.

This Cloud product is distinct from the Framework `Console` brand contribution.
The Framework contribution only projects runtime/read-model facts inside the
single Framework Cordis Host. It may provide typed client contributions for an
App Shell to render, but it does not own Cloud accounts, policy, quotas,
Workspace lifecycle, billing, Control Plane APIs, databases, or this product's
release. `opl-aion-shell` and `opl-studio` are App GUI carriers, not Console
authorities.

Console is not the package manager, Serve service-state owner, resource
executor, connector runtime, Ledger truth, or domain authority.

## MVP Boundary

Core Console is deliberately thin: it exposes only the Workspace collection and
the balance and usage facts needed to create and manage one local Docker
Workspace path through Control Plane. It consumes authoritative Sub2API and
Ledger projections and owns no wallet, usage store, or receipt truth.

Self-service signup, customer payment/top-up, detailed visual refinement,
managed-resource policy, and Serve administration are later or extension
surfaces. Current capability is owned by [status](status.md); gap and priority
are owned by the [roadmap](roadmap.md).

## Governance Objects

| Object | Purpose |
| --- | --- |
| Account | Identity, billing, policy, and resource boundary |
| Collaboration policy | Optional sharing of refs, artifacts, and approved resources without sharing Workspace ownership |
| Role | Permission bundle for collaboration, approval, administration, or audit |
| Workspace policy | How each account Workspace is created, accessed, renewed, suspended, deleted, and retained |
| Resource policy | Which compute, storage, connector, and environment refs are permitted |
| Package availability policy | Which exact OPL Package refs may be used by the account Workspace |
| Service policy | Which exact revisions may be published and under which consumer, data, quota, and retention policy |
| Service plan | How managed usage is attributed and billed; not an OPL Package |
| Approval policy | Which actions need approval and who can approve them |
| Audit policy | Which actions require receipts and retention |

## Approval Targets

Console may approve:

- Workspace creation, renewal, recovery, suspension, and deletion;
- connector credentials and explicitly managed access;
- environment, compute, and storage use;
- availability of an exact Package-owner publication ref with fresh carrier
  state;
- Agent Instance creation under account policy;
- creation, deployment, traffic promotion, pause, rollback, custom domain, and
  retention actions for an exact Agent Revision through Serve;
- budget, quota, retention, and reviewer-gate policy.

Package approval is a policy decision only. The Package owner controls identity
and publication bytes; the configured carrier controls install/update/remove
and native readback. Framework only delegates those actions and aggregates
their state.

## Service Plan Model

Billing plans are deliberately not called packages:

| Area | User-facing plan | Metered breakdown |
| --- | --- | --- |
| Gateway | AI usage plan | provider, model, tokens, requests |
| Workspace | Workspace service plan | instance, uptime, storage allocation |
| Serve | Agent Service plan | service, revision, deployment, endpoint, invocation/session |
| Compute | Standard or accelerated compute plan | adapter, duration, GPU flag |
| Storage | Workspace or private storage plan | allocation, retention, transfer signal |
| Connectors | Managed connector access | actions and policy events |
| Agents | Agent-run usage | exact package/revision ref, run, resource, and reviewer gate |

The first Serve commercial boundary bills the publisher account. Marketplace,
merchant-of-record, tax, refund, KYC, revenue sharing, and end-customer
subscription behavior are separate later product decisions.

## Metering And Billing Boundary

Console can meter Gateway provider usage, the managed Workspace plan, Serve
endpoint and invocation/session usage, Cloud-hosted compute and storage, and
explicitly managed connector usage. User-provided local, SSH, or HPC resources
can still produce Fabric and Ledger refs without becoming Cloud-billed by
default.

Gateway is the only spendable-balance owner. Console owns the account-total
billing view, pricing and settlement policy, and orchestrates one Workspace
monthly debit against that Gateway balance. Fabric owns no wallet or balance;
it returns resource and provider facts. Ledger records append-only charge,
refund, resource, and reconciliation receipts without becoming a second
spendable balance.

Attaching an exact package or revision ref to usage is attribution only. Console
cannot change the owner descriptor or publication revision, carrier installed
state, service runtime state, or domain readiness.

## Product Boundary

Ordinary users ultimately use Console for account onboarding, balance and usage, Workspace
creation and lifecycle, and support; they perform professional work in App or
Workspace. Administrators use Console to decide who may use or publish which
managed capability and under what budget or policy.
Serve performs Agent Service lifecycle actions, Runway owns Invocation/Session
execution, Fabric executes approved resource bindings, the configured carrier
performs Package mutations through Framework delegation, and Ledger records
refs. Domain owners retain professional quality and delivery authority.
