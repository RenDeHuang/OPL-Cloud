# Workspace Identity And External SaaS Boundary

Owner: `one-person-lab-cloud`
Purpose: `workspace_identity_decision`
State: `active_decision`
Machine boundary: Human-readable product decision; implementation and runtime
state come from the owning service and instance readback.

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

## Collaboration And Serve

Organizations and teams govern policy, approval, and collaboration around
independent Workspaces. OPL Serve publishes Agent Revisions as separate Agent
Services through its Agent Edge.
