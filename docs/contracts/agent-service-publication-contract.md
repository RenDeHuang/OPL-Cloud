# Agent Service Publication Contract

Owner: `one-person-lab-cloud`
Purpose: `agent_service_publication_planning_contract`
State: `active_target_contract`
Machine boundary: Human-readable planning contract. It is not an executable
service schema, deployed control plane, endpoint, runtime readback, or
production-readiness proof.

This planning contract defines the target handoff from an exact OPL Agent Package
to an externally usable OPL Serve service. It is a human-readable product and
ownership contract, not an active runtime schema or deployment-readiness claim.

## Invariants

1. The Package owner remains the only identity, capability, entrypoint and
   publication-revision authority. The configured native carrier remains the
   physical install/update/remove and installed/callable authority; Framework
   only delegates and aggregates.
2. OPL Serve owns service publication state without creating another Agent
   Registry, resolver, lock or carrier-state copy.
3. Public traffic terminates at the Serve Agent Edge, never at a Workspace,
   sandbox, container, or provider session directly.
4. API, Embed, and Hosted UI modes use the same invocation, session, policy,
   quota, event, and receipt contracts.
5. Runway owns the OPL invocation/session lifecycle; provider identifiers remain
   external refs.
6. Execution success does not grant a professional quality, publication, export,
   or delivery verdict.

## Object And Owner Model

| Object | Required identity | Owner |
| --- | --- | --- |
| Agent Package | package id, owner descriptor ref and exact publication revision/digest | Package owner |
| Carrier state | configured carrier id and fresh installed/callable readback | Native carrier; Framework delegates and aggregates |
| Service Entrypoint | action/stage ref, I/O schemas, modes, permissions and resource policy | Package/domain owner contract |
| Agent Service | service id, publisher account, display identity and lifecycle policy | OPL Serve |
| Agent Revision | immutable package closure, entrypoint, configuration, policy and provider refs | OPL Serve references owner truths |
| Deployment | environment, desired revision set, traffic and rollback policy | OPL Serve |
| Invocation | request identity, consumer principal, exact revision and bounded outcome | OPL Runway; Serve projects status |
| Session | stateful event sequence against an exact revision | OPL Runway; Serve projects status |
| Receipt | deployment, invocation/session, policy, output and cost refs | OPL Ledger |

## Service Entrypoint

A package may be publishable only when it declares a portable service entrypoint.
The entrypoint planning shape includes:

- `entrypoint_ref`: exact action or stage binding;
- `input_schema_ref` and `output_schema_ref`;
- `supported_invocation_modes`: bounded synchronous, asynchronous, or stateful
  session;
- `event_schema_ref` for progress, output, tool, approval and terminal events;
- `timeout_and_cancellation_policy_ref`;
- `permission_and_side_effect_policy_ref`;
- `resource_profile_ref`;
- `connector_and_egress_policy_ref`;
- `data_classification_and_retention_policy_ref`;
- `artifact_and_delivery_policy_ref`;
- `review_and_owner_route_ref`.

OMA or another Agent-development method may generate this declaration and its
acceptance inputs. It cannot create canonical Service, Revision, Deployment, or
traffic state.

## Agent Service

An Agent Service is a publisher-facing product resource. Its planning fields are:

- `service_id`;
- `owner_account_ref`;
- `display_name` and optional publisher branding refs;
- `service_status`;
- `consumer_auth_policy_ref`;
- `quota_and_rate_limit_policy_ref`;
- `billing_policy_ref`;
- `data_and_retention_policy_ref`;
- `hosted_ui_policy_ref`;
- `custom_domain_policy_ref`;
- `created_at` and `updated_at`.

Service identity is stable across revisions. It does not imply that any revision
is deployed or ready.

## Immutable Revision

An Agent Revision binds exact inputs that must not drift after publication:

- `revision_id` and `service_id`;
- Package owner descriptor and exact publication revision/digest refs;
- required/optional identity refs and fresh callable evidence where needed;
- service entrypoint and schema refs;
- configuration digest and secret refs, never secret values;
- Gateway, Fabric, Connect and Ledger policy refs;
- execution-provider kind and provider configuration ref;
- data, egress, retention and artifact policies;
- revision creation and validation receipt refs.

A package update creates a new Agent Revision. It cannot mutate the bytes or
configuration represented by an existing revision.

## Deployment And Traffic

A Deployment binds one service to one environment and desired traffic policy:

- `deployment_id`, `service_id`, and `environment_ref`;
- exact revision refs and traffic weights;
- desired and observed status;
- health, rollout, pause, rollback and retention policies;
- endpoint, custom-domain and certificate refs;
- runtime-provider and Fabric binding refs;
- deployment receipt and rollback target refs.

Supported lifecycle intent is:

```text
draft -> validating -> deploying -> active -> paused -> archived
                         \-> failed
```

Traffic promotion requires revision validation and live health evidence. Package
presence, a generated endpoint, or a successful sandbox start is not sufficient.

## Invocation And Session

Every external request is bound to a consumer and an exact revision. The request
shape includes:

- `request_id` and `idempotency_key`;
- `service_ref`, `deployment_ref`, and resolved `revision_ref`;
- `consumer_principal_ref` and authentication policy ref;
- `input_refs` or validated inline input according to the data policy;
- requested invocation mode;
- callback or event-stream policy;
- deadline, cancellation and cost policies.

The response returns either a bounded result or an `invocation_ref`/`session_ref`
with status, event-stream, output, artifact, cost-signal and Ledger receipt refs.
Long-running work must not hold an unbounded synchronous HTTP request open.

## Public Edge And Client Contract

The Agent Edge owns public authentication, short-lived browser credentials,
request validation, tenant/service routing, rate limits, quotas, idempotency,
bounded payloads, event streaming, signed Webhooks, abuse controls and endpoint
observability.

Publisher server keys are not browser credentials. Hosted UI and Embed clients
must obtain scoped, short-lived consumer credentials and cannot receive provider,
Gateway, connector, or service-owner secret values.

## Hosted UI Projection

A Hosted UI is generated from an exact service/revision projection:

- publisher and service branding refs;
- input, output, event and artifact schemas;
- supported interaction template;
- consumer authentication mode;
- upload, retention and delivery policies;
- help, privacy and support links.

The projection may change appearance without changing invocation semantics. A UI
cannot call Fabric, Runway, Gateway, or a provider adapter around the Serve API.

## Execution Provider Port

The Runway provider port must hide provider-specific lifecycle details behind
the OPL Invocation and Session contract. Required behavior includes start,
event input, event observation, status, interrupt/cancel, output collection and
provider cleanup.

The OPL-native Runway/Fabric path and any approved external managed-Agent path
are separate adapters. Provider Agent, Environment, Session, Event, Deployment,
or sandbox identifiers are recorded as refs and never become OPL service truth.

## Failure And Owner Routing

| Failure | Owner route |
| --- | --- |
| Missing, invalid or stale owner publication ref | Package owner |
| Missing or unusable physical carrier state | Configured native carrier / Framework adapter |
| Invalid entrypoint or schema | Package/domain owner |
| Consumer authentication, quota or abuse block | OPL Serve / OPL Console policy |
| Missing compute, storage, environment or network binding | OPL Fabric |
| Model access or model-usage failure | OPL Gateway |
| Connector or external-system failure | OPL Connect / source owner |
| Invocation/session orchestration failure | OPL Runway / provider adapter |
| Professional quality or delivery failure | Domain owner / publisher |
| Receipt persistence or readback failure | OPL Ledger |

## Evidence Boundary

Deployment and invocation receipts should bind exact package, revision,
deployment, consumer-policy, provider, resource, model-usage, input, output,
artifact, review, cost and continuation refs where applicable.

Contract completeness, a generated Hosted UI, endpoint allocation, successful
tests, or provider session creation cannot be described as a delivered public
service. Production claims require exact runtime, isolation, security, billing,
rollback, deletion, soak and user-path evidence from the owning implementations.

## Explicit Non-Goals

- No Cloud-owned Agent Registry or package lifecycle copy.
- No Workspace share URL as an Agent Service endpoint.
- No public port exposed directly from a sandbox or provider session.
- No separate execution behavior for Hosted UI or Embed clients.
- No generic website builder in the initial Serve scope.
- No marketplace, merchant-of-record, tax, KYC, refund, or revenue-sharing claim.
