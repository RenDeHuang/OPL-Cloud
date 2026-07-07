# OPL Cloud Commercial Console Route Contract

## Brief

Use Product Design to rebuild OPL Cloud as a commercial console, not a demo page. The UI must be clean, visual, and sparse: less explanatory copy, more status, tables, actions, and focused empty states.

The implementation stays on `feature/user-resource-billing`. Main must remain untouched.

## Route Contract

Public and auth routes:

```text
/                         Public home
/pricing                  Pricing
/docs                     Docs
/status                   Service status
/legal/terms              Terms
/legal/privacy            Privacy
/setup                    First-run setup

/login                    Login
/register                 Register, feature gated
/invite/accept            Accept invite
/email/verify             Email verification
/forgot-password          Forgot password
/reset-password           Reset password
/auth/callback            SSO/OAuth callback
/logout                   Logout
```

Console routes:

```text
/console                  -> /console/overview
/console/overview         Overview

/console/workspaces       Workspace list
/console/workspaces/new   Create Workspace
/console/workspaces/:id   Workspace detail
/console/workspaces/:id/access     URL and access
/console/workspaces/:id/resources  Compute and storage
/console/workspaces/:id/backups    Backups and restore
/console/workspaces/:id/receipts   Workspace receipts

/console/gateway          OPL Gateway
/console/gateway/keys     Access keys
/console/gateway/usage    Gateway usage
/console/gateway/quotas   Quotas and limits

/console/billing          Billing overview
/console/billing/wallet   Wallet and holds
/console/billing/usage    Compute, storage, and request usage
/console/billing/orders   Top-up and order records
/console/billing/invoices Invoices

/console/account          Account and lab
/console/account/profile  Profile
/console/account/security Login security
/console/account/lab      Lab ownership, members, policy summary
/console/account/alerts   Notifications and balance alerts

/console/support          Support tickets
/console/support/new      New ticket
/console/support/:id      Ticket detail

/console/resources        Resource catalog, hidden from primary menu
/console/resources/connectors    Approved connectors
/console/resources/environments  Approved environments
/console/resources/agents        Approved agent packages
/console/approvals        Approvals, hidden from primary menu
/console/receipts         Human-readable receipts
/console/alerts           Alerts
```

Admin routes:

```text
/admin                    -> /admin/overview
/admin/overview           Admin overview

/admin/users              User management
/admin/users/new          Create user
/admin/users/:id          User detail
/admin/users/:id/wallet   Top up, deduct, freeze, and balance history
/admin/users/:id/workspaces User workspaces
/admin/users/:id/usage    User usage
/admin/users/:id/audit    User audit

/admin/governance         Governance overview
/admin/governance/organizations Organizations and labs
/admin/governance/teams          Teams
/admin/governance/roles          Roles and permissions
/admin/governance/policies       Quota, approval, and audit policies

/admin/workspaces         All Workspaces
/admin/workspaces/:id     Admin Workspace detail

/admin/billing            Billing operations
/admin/billing/plans      Plan management
/admin/billing/topups     Manual top-up records
/admin/billing/transactions Wallet transactions
/admin/billing/reconciliation Reconciliation

/admin/gateway            Gateway management
/admin/fabric             Fabric management
/admin/fabric/compute     Compute resources
/admin/fabric/storage     Storage resources
/admin/fabric/connectors  Connector approval
/admin/fabric/environments Environment templates
/admin/fabric/agents      Agent package approval

/admin/ledger             Ledger management
/admin/ledger/receipts    Receipts
/admin/ledger/events      Raw Ledger events
/admin/ledger/policies    Retention and review policies

/admin/runtime            Runtime
/admin/runtime/readiness  Production readiness
/admin/runtime/kubernetes TKE/K8s
/admin/runtime/images     Workspace images
/admin/runtime/domains    Domains and Ingress

/admin/support            Support ticket management
/admin/support/:id        Support ticket handling
/admin/audit              Audit log
/admin/settings           Settings
/admin/alerts             Global alerts
```

Fallback routes:

```text
/403                      Forbidden
/404                      Not found
/500                      Error
/*                        -> /404
```

## Menu Contract

Lab Owner primary menu:

```text
Overview
Workspaces
Gateway
Billing
Account & Lab
Support
Alerts
```

Admin adds:

```text
Admin Overview
Users
Governance
All Workspaces
Billing Ops
Gateway Ops
Fabric
Ledger
Runtime
Support Ops
Audit
Settings
```

Do not expose these in the Lab Owner primary menu:

```text
Raw Ledger events
Runtime evidence
Production readiness
TKE/K8s
Manual settlement
Request fingerprints
Dedup rows
Manual top-up execution
```

## Interaction Contract

- All primary routes must be clickable and restore on refresh.
- Use lazy-loaded route components.
- Route metadata must include role visibility, menu visibility, and feature-gate intent.
- Workspaces must make URL copy/open/reset/delete visible as row or detail actions.
- Create compute/storage must use confirmation with package, price, seven-day hold, and balance sufficiency; Create Workspace validates an attached compute/storage pair and generates the URL entry.
- Billing must explain wallet, holds, usage, and recent charges without dumping raw Ledger data.
- Manual top-up is executed from `Users -> User detail -> Wallet`; top-up records are viewed from Billing Ops.
- Support tickets must support list, new ticket, and detail views.

## Product Design Rules

- Commercial UI, not demo UI.
- Sparse copy. Prefer cards, tables, status tags, timelines, and direct actions.
- Avoid dense walls of panels.
- Keep Lab Owner focused on Workspace delivery and billing clarity.
- Keep operator evidence in Admin only.
- Use Ant Design and ProComponents on React + Vite.
- Follow Sub2API's route/auth/admin separation, not its Vue stack.

## Test Contract

Replace the legacy Chinese IA lock test with route and permission contract tests:

- route contract includes public, auth, console, support, and admin routes;
- Lab Owner menu excludes admin/runtime/raw ledger/manual top-up internals;
- Admin menu includes users, wallet top-up, audit, runtime, Fabric, Ledger, and support ops;
- support ticket routes exist;
- commercial UI source does not expose `结算 1 小时`, `requestUsageDedup`, `requestFingerprint`, or `production readiness` in Lab Owner route definitions.
