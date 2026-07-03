# Project Scope

This repository implements the OPL Cloud control-plane slice for OPL Console and OPL Workspace.

## External Sources

- `one-person-lab` owns the development framework: goal, attempt, readiness, receipt, blocker, next step, human gate, recovery, evidence, docs lifecycle, and contract-light rules.
- `one-person-lab-cloud` owns the product definition: OPL Gateway, OPL Workspace, OPL Console, OPL Fabric, and OPL Ledger.

## Repository Ownership

This repository owns:

- OPL Console account, billing, access, lifecycle, support, and admin surfaces.
- OPL Workspace provisioning and URL delivery control plane.
- Local Docker and Tencent TKE runtime handoff for v1.
- Persistent workspace storage lifecycle, backup, restore-to-new-Workspace, and retention contracts.
- User wallet, resource usage, request usage, manual top-up audit, billing ledger, and reconciliation records.
- Runtime readiness, production readiness, production manifests, and TKE deployment handoff.

## Out Of Scope

This repository does not own:

- OPL Gateway internals.
- One Person Lab framework internals.
- `one-person-lab-app` WebUI behavior.
- Full standalone OPL Ledger service internals.
- Domain evidence judging, artifact storage, or agent run registry internals.
- Connector, environment, and agent marketplaces beyond approved catalog boundaries.

## Development Rule

Before adding a check or feature, classify it as:

1. One Person Lab framework rule.
2. OPL Cloud product rule.
3. This repository's Console / Workspace implementation rule.

Implement it here only when this repository owns the rule or the integration boundary.
