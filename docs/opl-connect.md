# OPL Connect

Owner: `one-person-lab-cloud`
Purpose: `connect_target_reference`
State: `active_target_reference`
Machine boundary: Human-readable target connector reference; implementation and
readiness come from Framework, domain, and owner readback.

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

## Transport And Domain Boundary

Generic shared providers belong in OPL Connect when their access semantics are
stable across domains. Current Framework source exposes Crossref, OpenAlex,
PubMed, and Europe PMC through OPL Connect's read-only scientific search and
reference-verification transport. OPL Connect owns provider invocation, retry,
cache, identifier and metadata normalization, source refs, and transport
receipts for those routes.

MAS and other domain owners consume the exact provider/source refs and retain
query strategy, result selection, evidence interpretation, claim support, and
quality decisions. Current provider or domain readiness requires fresh
Framework, provider, and domain owner readback.

## Governance And Evidence

| Concern | Owner |
| --- | --- |
| Connector access, refs, credentials, provider errors/retries/rate limits | OPL Connect |
| Adapter Package identity, capabilities and publication revision | Package owner |
| Adapter physical install, update, remove and readback | Configured native carrier; Framework delegates and aggregates |
| Account/service availability, credential approval, quota and audit policy | OPL Console |
| Resource and environment binding | OPL Fabric |
| Retrieval strategy, evidence use, writing and review | Domain Agent |
| Receipt and opaque provenance refs | OPL Ledger; the calling owner retains continuation authority |

Connector availability, package health, policy approval and domain readiness
are separate states and must remain separately readable.
