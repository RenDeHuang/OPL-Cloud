# OPL Agent Lifecycle In Cloud

Owner: `one-person-lab-cloud`
Purpose: `cloud_agent_lifecycle_reference`
State: `active_target_reference`
Machine boundary: Human-readable cross-surface object and owner model. Actual
package, carrier, service, invocation, resource, receipt, and domain truth stays
with the owning Package, carrier, Framework, service, runtime, and domain
surfaces.

OPL Cloud can expose standard OPL Agents in App/Workspace and publish them
through OPL Serve without owning a second agent package platform. Agent design,
package publication, carrier lifecycle, account policy, service publication,
resource binding, execution and evidence remain separate responsibilities.

```text
Agent design / domain source
-> Agent Package candidate
-> owner descriptor + exact publication revision
-> configured native carrier install + installed/callable readback
-> Framework discovery and state aggregation
-> optional Console account availability policy
-> App / Workspace Agent Instance -> Agent Run
or
-> Service Entrypoint Contract
-> OPL Serve Agent Service -> immutable Agent Revision -> Deployment
-> OPL Runway Invocation / Session
-> Fabric/provider resource binding
-> Ledger receipt refs
```

## Lifecycle Objects

| Object | Meaning | Owner |
| --- | --- | --- |
| Agent design | Goal, boundary, stages, inputs, outputs, review rules and authority functions | OMA / domain owner |
| Agent Package | Versioned distributable candidate and its owner source | Package-owning repo |
| Package descriptor and publication revision | Stable identity, capabilities, entrypoints and exact published bytes | Package owner |
| Carrier installation | Physical bytes and fresh installed/callable state | Configured native carrier; Framework aggregates and delegates |
| Account availability policy | Which package refs the account Workspace may use | OPL Console |
| Resource binding | Compute, storage, environment and connector bindings for one instance or run | OPL Fabric |
| Agent Instance | A package ref exposed in App or Workspace with explicit permissions and resources | OPL App / Workspace |
| Agent Run | One execution with output and review refs | Runtime owner; Ledger records refs |
| Service Entrypoint | Portable action/stage, I/O, event, permission, side-effect and data-policy declaration | Package/domain owner contract |
| Agent Service | Stable publisher-owned external service identity | OPL Serve |
| Agent Revision | Immutable package digest, entrypoint, configuration, policy and provider refs | OPL Serve references owner truths |
| Deployment | Desired revision set, environment, endpoint and traffic policy | OPL Serve |
| Invocation / Session | Bounded request or stateful event sequence against an exact revision | OPL Runway; Serve projects status |

## Product Flow

1. OMA or another domain owner produces an Agent Package candidate.
2. The Package owner publishes an exact revision whose descriptor declares
   stable identity, capabilities, entrypoints and resource requirements.
3. The configured native carrier installs, updates or removes physical bytes
   and returns fresh installed/callable state; Framework delegates the action
   and aggregates the readback without creating a second lifecycle authority.
4. App or Workspace can expose the exact package as an Agent Instance for
   workbench use.
5. A publishable package may additionally declare a Service Entrypoint Contract.
6. Serve creates a stable Agent Service and immutable Revision referencing the
   exact package digest. It does not copy or mutate package state.
7. Console applies account service, quota, budget, data and retention policy.
8. Serve deploys the revision behind its Agent Edge. Runway owns Invocation and
   Session execution and selects an approved provider adapter.
9. Fabric consumes package, revision and policy refs to bind approved compute,
   storage, environments, secrets, network and connectors.
10. Ledger records package, service, deployment, invocation/session, resource,
    output, review and continuation refs as opaque provenance without becoming
    package, service, continuation-authority, or domain truth.

## Failure And Repair

Publication or descriptor failures route to the Package owner. Download,
install, update, remove and repair failures route to the configured native
carrier through its Framework adapter. A failed Fabric binding is a resource
failure; a denied Console policy is an account-policy result; neither is
permission to rewrite Package identity or carrier state. A failed provider
session routes through Runway; a failed deployment routes through Serve.
Neither may be repaired by changing an immutable Revision in place.

## Authority Boundary

- Package identity present, carrier callable or resource binding successful
  does not mean the domain Agent is ready or its output is professionally
  valid.
- Console policy approval does not approve package bytes or domain quality.
- Service publication or endpoint allocation does not prove a deployable or
  production-ready Agent Service.
- Fabric execution success does not produce an owner verdict.
- Ledger receipt presence does not replace package, resource or domain truth.
