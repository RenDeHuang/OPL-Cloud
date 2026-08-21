# OPL Workspace

Owner: `one-person-lab-cloud`
Purpose: `workspace_target_reference`
State: `active_target_reference`
Machine boundary: Human-readable target product reference; implementation and
readiness come from Workspace source, tests, status, and instance readback.

OPL Workspace is the cloud OPL App workbench. It should feel like the same
project, task, artifact, review and delivery experience in an online deployment,
not like a container hosting panel.

## User Model

| Term | Meaning |
| --- | --- |
| OPL Workspace | One independently addressable user-visible cloud workbench |
| Workspace Instance | An OPL App container deployment plus WebUI, with its own stable identity and runtime binding |
| Workspace Storage | Project files, volumes, buckets, outputs and delivery space attached to an instance |

One user account may own zero or more independent OPL Workspaces. The product
does not impose a fixed count limit; each creation is admitted independently by
balance, provider capacity, quota, and policy. Workspace state is keyed by
`workspace_id`, never by an account singleton.

## MVP Boundary

The first Workspace carrier is an OPL App/WebUI Docker container created and
managed through the real product chain:

```text
Console -> Control Plane -> Workspace launcher/provider -> local Docker
```

Core completion requires create, authoritative readback, access, and delete on
a supported Linux Docker host. Compose startup of the Cloud control services
does not satisfy this boundary. The Local-Docker provider exists, but one
exact-current clean-host product journey remains open; see
[current capability](status.md) and the current [P0 gaps](roadmap.md).

## Workspace Product Flow

```text
open the account Workspace list
-> create or select one Workspace
-> select resource profile and permitted OPL Package refs
-> provision access, storage and runtime
-> open project workbench
-> run tasks or Agent Instances
-> optionally publish an exact Agent Package revision to OPL Serve
-> inspect artifacts, reviews and receipts
-> suspend, resume or delete according to policy
```

## Workspace Contents

A Workspace can show projects, task sessions, files, storage, Agent Instances,
job status, resource use, artifacts, review status, Ledger refs and
continuation entries. Framework supplies package state and actions from owner
descriptors and fresh native-carrier readback.

Workspace may expose a **Publish to OPL Serve** action after the package,
entrypoint, policy and owner gates are satisfied. The action calls Serve owner
surfaces, which create canonical Service, Revision, Deployment, endpoint, and
traffic state.

## Responsibility Boundary

- Package owners control identity, capabilities, entrypoints and exact
  publication revisions.
- Configured native carriers control physical install, update, remove and fresh
  installed/callable readback; Framework delegates and aggregates.
- Console owns account availability, quota, and policy across the account's Workspace collection.
- Serve owns Agent Service publication, immutable revisions and external endpoints.
- Fabric binds and runs compute, storage, environment and connector resources.
- Gateway supplies AI access.
- Ledger records receipt and opaque provenance refs; Workspace retains project,
  artifact, review, and continuation authority.
- Workspace presents the user experience and dispatches owner actions.

A Workspace URL serves workbench access; an Agent Service endpoint serves
external consumers. Both collections have zero-to-many account cardinality and
separate product identities and lifecycles.

Package availability, resource availability and domain readiness are different
states. Workspace must display their owner and next action rather than collapse
them into a single ready flag.
