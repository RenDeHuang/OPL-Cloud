# OPL Serve

Owner: `one-person-lab-cloud`
Purpose: `serve_target_reference`
State: `active_target_reference`
Machine boundary: Human-readable target product definition.

OPL Serve is the planned OPL Cloud product for publishing a validated OPL Agent
as an externally usable service. It gives Agent builders a stable API and, when
needed, an OPL-hosted user interface through a managed Agent Edge.

## Product Promise

An Agent builder can use OMA or another compatible method to produce an Agent
Package, publish an exact owner revision, and then bind that revision to OPL
Serve.

External consumers can use that service in three ways:

| Mode | Consumer experience | Product boundary |
| --- | --- | --- |
| API | The publisher connects the Agent to an existing App, site, or backend | OPL Serve exposes authenticated invocation and session APIs |
| Embed | The publisher embeds an OPL-provided interaction component | The component uses the same public API and short-lived client credentials |
| Hosted UI | OPL hosts a branded task, report, workflow, or chat surface | The UI is an official API client, not a second execution path |

The publisher may operate several Agent Services. Those services are deployment
resources, not OPL Workspaces. An account may independently own zero or more
Workspaces and zero or more Agent Services; the two collections keep separate
identity, lifecycle, access, and billing contracts.

## Brand And Product Language

| Term | Meaning |
| --- | --- |
| OPL Serve | The user-visible Agent publishing and serving product |
| Agent Service | A publisher-owned external service based on an exact Agent Package revision |
| Agent Revision | An immutable package digest, entrypoint, configuration, policy, and provider binding |
| Deployment | The desired serving state and traffic policy for one revision or revision set |
| Invocation | One bounded request to a service |
| Session | A stateful sequence of events against a service revision |
| Hosted UI | An optional OPL-hosted client for the same public service API |

The preferred publisher action is **Publish to OPL Serve**. External consumers
primarily see the publisher's Agent name and brand. A secondary `Hosted on OPL
Cloud` attribution may be shown. Model-provider attribution remains optional and
must follow that provider's branding rules.

## Publication Flow

```text
Agent design / domain source
-> Agent Package candidate
-> owner descriptor + exact publication revision
-> Service Entrypoint Contract
-> Agent Service
-> immutable Agent Revision
-> Deployment and traffic policy
-> API / Embed / Hosted UI
-> Invocation or Session
-> Ledger receipt refs
```

OPL Serve references the exact Package owner publication revision and owns the
service publication state. The native carrier owns installed state.

## Serving Architecture

```text
consumer App / site / Hosted UI
-> OPL Serve Agent Edge
-> service authentication, quota, rate limit and routing
-> OPL Runway invocation or session lifecycle
-> execution provider
-> OPL Fabric resources + OPL Gateway + OPL Connect
-> outputs, events and artifacts
-> OPL Ledger receipt refs
```

The Agent Edge terminates public traffic. A sandbox, container, Workspace URL, or
provider session is never exposed directly as the stable public endpoint.

Short actions may return synchronously within a bounded timeout. Long-running
work returns an `invocation_ref` and continues through event streaming, polling,
or signed Webhooks. Stateful interaction uses an explicit Session rather than
turning every request into an unrelated run.

## Hosted UI Boundary

Initial Hosted UI templates should cover repeatable Agent interaction patterns:

- Task: structured input, background progress, completion, and notification.
- Report: material upload, generated artifacts, review status, and delivery.
- Workflow: multi-stage progress, approval points, outputs, and history.
- Chat: stateful turns, streaming events, file exchange, and interruption.

Templates consume the public Serve API and service entrypoint schemas. They may
project publisher name, logo, theme, domain, authentication mode, inputs,
outputs, events, artifacts, and support links. Public authentication and quota
policy apply to every template.

Browser clients use short-lived consumer credentials. A publisher's server-side
service key or provider credential must never be shipped to a browser template
or embed component.

## Ownership

| Concern | Owner |
| --- | --- |
| Agent design, professional behavior, quality and delivery authority | OMA / domain owner / Agent publisher as applicable |
| Package identity, capabilities, entrypoints and publication revision | Package owner |
| Physical install, update, remove and installed/callable readback | Configured native carrier; Framework delegates and aggregates |
| Agent Service, Revision, Deployment, endpoint and traffic state | OPL Serve |
| Account policy, service allowance, budget, quota and billing projection | OPL Console |
| Invocation and Session execution lifecycle | OPL Runway |
| Sandbox, compute, storage, environment, secret injection and network binding | OPL Fabric |
| Model access, provider policy, routing and model usage | OPL Gateway |
| External data and tool access | OPL Connect |
| Invocation, deployment, policy, output and cost receipt refs | OPL Ledger |

## Provider Model

OPL Serve is provider-neutral. Runway exposes one execution-provider port. The
initial path uses Runway orchestration and Fabric-managed execution; another
adapter is added only for a current provider requirement.

OPL Service, Revision, Deployment, Invocation, Session, and Ledger identifiers
remain canonical across providers. External Agent, Environment, Session, Event,
and scheduled-deployment identifiers are provider refs.

## Data And Security Boundary

Each revision declares input/output schemas, permissions, side effects, resource
requirements, data classification, retention, egress, cancellation, and
artifact policies. Serve and Console apply the publisher's account policy before
traffic reaches Runway or Fabric.

Hosted execution does not imply that all data may leave its owner boundary.
Provider eligibility must be selected from the declared data policy. Sensitive
or regulated workloads may require an OPL-native, institution-owned, or otherwise
approved execution path even when another provider offers a managed sandbox.

## Commercial Boundary

The first commercial boundary bills the publisher account for service, model,
execution, storage, and managed connector usage.

## Currentness

Current implementation evidence belongs in [status](status.md); an accepted
Serve implementation outcome belongs in the [roadmap](roadmap.md).
