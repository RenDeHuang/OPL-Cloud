# OPL Gateway

Owner: `one-person-lab-cloud`
Purpose: `gateway_target_reference`
State: `active_target_reference`
Machine boundary: Human-readable target product definition. It does not prove
a Gateway implementation, model availability, provider state, usage, billing,
release, or production readiness.

OPL Gateway is the target frontier-AI capability gateway for One Person Lab.

Its target responsibilities are unified AI API access, credential and provider
configuration, usage metering, and downstream OPL workflow integration.

## Role In OPL Cloud

OPL Gateway is the first capability foundation in the target Cloud delivery
order and remains a top-level product surface. Its planned consumers include:

- OPL App AI capability access.
- Codex and automation workflow integration.
- OPL Serve invocation and session model access.
- Usage visibility and quota management.
- A stable foundation for Console billing and account policy.

## Usage Model

Gateway usage should be groupable by:

- account;
- user, publisher service or consumer identity;
- Workspace;
- Agent Service, Revision, Deployment and Invocation/Session;
- task or job;
- exact OPL Package ref or Agent Instance;
- provider and model.

This gives Console enough information for quotas, budgets, service/package
attribution and downstream reporting without making Gateway responsible for
package lifecycle, service deployment or industry-specific policy.

Gateway is technically a resource access capability, but it should not be
hidden inside OPL Fabric. Users can directly configure it, use it, meter it, and
pay for it.

## Public Surface

This repository currently defines only the target integration surface. A
dedicated implementation owner, machine contract, runtime readback, and release
evidence are required before availability can be claimed.

## Positioning

OPL Gateway should be described as OPL Cloud's AI capability foundation, not as
a generic token platform.

## Boundary With Fabric

OPL Fabric owns general connectors, compute, storage, environments, and
execution adapters. OPL Gateway owns frontier AI access, provider policy, model
routing, keys, and usage metering. OPL Serve owns the external Agent endpoint;
it does not turn Gateway into the Agent Service control plane.
