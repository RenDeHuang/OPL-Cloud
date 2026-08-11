# Workspace Identity And External SaaS Boundary

Owner: `one-person-lab-cloud`
Purpose: `workspace_identity_decision`
State: `active_decision`
Machine boundary: Human-readable product decision and owner exclusion. It does
not prove a Workspace implementation, account state, collaboration policy,
runtime, or release readiness.

This document records the current OPL Cloud product decision. It is a planning
boundary, not a delivery or runtime-readiness claim.

## Workspace Identity

The canonical Cloud identity rule is:

```text
1 user account -> 0..N independent OPL Workspaces
```

- OPL App is the user's local workbench and OPL Workspace is the cloud form of
  the same workbench model.
- Each Workspace has a stable `workspace_id`, URL, runtime, storage, resource
  binding, credentials, billing period, lifecycle, and receipt chain.
- OPL Cloud sets no fixed product-level Workspace count limit. Balance,
  provider capacity, quota, and account policy can still admit or reject each
  creation independently.
- Projects, tasks, files, artifacts and continuation entries live inside their
  selected Workspace. They do not become Workspace identity.
- Collaboration may share refs, artifacts, approved resources and policy, but
  it does not merge independently owned Workspaces into a shared SaaS account.
- Console may manage account billing, quotas, approvals and managed resources.
  It lists and governs every Workspace owned by the account without becoming
  the runtime or provider-state owner.
- OPL Serve may let the account publish multiple Agent Services. A Service is an
  externally callable deployment resource, not another Workspace, browser
  workbench, project container or collaboration account.

The browser carrier for this path is the OPL App WebUI implementation provided
through the active App shell. It consumes App, Framework and domain-owner
projections. A browser renderer or transport may not own a second task,
package, artifact or Workspace state model.

## External Collaboration Repositories

The following repositories are external collaboration history, not canonical
OPL Cloud implementation repositories:

| Repository | External role | OPL maintenance status |
| --- | --- | --- |
| `RenDeHuang/OPL-Webui` | Multi-user browser SaaS and Web control-plane experiment | Not owned, not an OPL Cloud carrier, excluded from the maintained repo set |
| `RenDeHuang/MedOPL` | Companion commercial resource control plane for that SaaS experiment | Not owned, not an OPL Cloud owner surface, excluded from the maintained repo set |

These repositories must not be used as OPL App WebUI truth, OPL Workspace
identity truth, Cloud architecture authority, default audit scope or an OPL
family maintenance target. Useful implementation lessons may be reintroduced
only through an explicit, owner-reviewed intake into a current OPL-owned
surface.

Similarly named third-party or collaborator repositories are not adopted by
name. The canonical planning owner remains
`gaofeng21cn/one-person-lab-cloud`; App WebUI behavior remains owned by
`gaofeng21cn/one-person-lab-app` and its active shell carrier.

## Reading Older Team Language

Older Cloud documents may describe one primary Workspace, organizations, teams,
or shared resources. The one-primary-Workspace rule is superseded: the current
identity is zero-to-many independent Workspaces per account. Organization and
team language still describes optional policy, approval, and collaboration; it
does not merge Workspace identity or authorize a separate multi-tenant Web
product.

This non-goal does not prohibit OPL Serve. Serve exposes an exact Agent Revision
through a dedicated Agent Edge and optional API client templates. It does not
turn an account Workspace into a multi-tenant SaaS workbench or reuse a
Workspace URL as an external service endpoint.
