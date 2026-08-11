# OPL Connect

Owner: `one-person-lab-cloud`
Purpose: `connect_target_reference`
State: `active_target_reference`
Machine boundary: Human-readable target connector definition. It does not prove
provider access, adapter availability, package health, source truth, domain
readiness, or production readiness.

OPL Connect is the target connector capability inside OPL Fabric. It defines a
stable access boundary for App, Workspace, and approved domain agents to use
external data sources, literature providers, databases, tool APIs, and
institutional systems. Serve invocations may use the same target capability
through Runway and Fabric when an exact revision and consumer data policy permit
it.

## Connector Responsibility

Connect owns:

- connector request/result envelopes;
- credential and data-egress boundaries;
- provider-specific access behavior;
- normalized source refs;
- errors, retries, cache metadata and rate limits;
- provider execution receipts.

Connect does not own:

- OPL Package identity/publication, carrier install/update/remove/repair, or
  Framework discovery/state aggregation;
- domain retrieval strategy, inclusion judgment or evidence synthesis;
- source truth, professional quality verdict or delivery authority;
- account/service approval, quota or billing policy.

## Standard Call Shape

```text
App / Workspace / domain Agent
-> capability request
-> OPL Connect provider adapter
-> normalized source refs + provider receipt
-> domain workflow
-> optional Ledger receipt refs
```

Console may approve account-managed credentials, providers, service egress and
quotas.
Ledger may retain connector and source refs. Neither changes connector or
domain truth.

## Package Boundary

A connector adapter can be carried by an OPL Package. Its owner controls
identity, capabilities and exact publication revisions; the configured carrier
installs and reads back physical bytes. Framework aggregates the resulting
installed/callable adapter ref. Connect only invokes that ref, while Fabric
binds the adapter and its credentials/resources to a run.

This separation prevents connector catalogs and package registries from
becoming two competing descriptions of the same installed bytes.

## Transport And Domain Boundary

Generic shared providers belong in OPL Connect when their access semantics are
stable across domains. Current Framework source exposes Crossref, OpenAlex,
PubMed, and Europe PMC through OPL Connect's read-only scientific search and
reference-verification transport. OPL Connect owns provider invocation, retry,
cache, identifier and metadata normalization, source refs, and transport
receipts for those routes.

MAS and other domain owners consume the exact provider/source refs and retain
query strategy, result selection, evidence interpretation, claim support, and
quality decisions. This ownership boundary does not prove that any provider is
configured, reachable, current, or production ready; those states require
fresh Framework and domain readback.

## Governance And Evidence

| Concern | Owner |
| --- | --- |
| Connector access, refs, credentials, provider errors/retries/rate limits | OPL Connect |
| Adapter Package identity, capabilities and publication revision | Package owner |
| Adapter physical install, update, remove and readback | Configured native carrier; Framework delegates and aggregates |
| Account/service availability, credential approval, quota and audit policy | OPL Console |
| Resource and environment binding | OPL Fabric |
| Retrieval strategy, evidence use, writing and review | Domain Agent |
| Receipt/provenance refs and continuation entry | OPL Ledger |

Connector availability, package health, policy approval and domain readiness
are separate states and must remain separately readable.
