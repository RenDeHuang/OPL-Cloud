# Resource Ownership And Billing

Owner: `one-person-lab-cloud`
Purpose: `resource_ownership_billing_reference`
State: `active_target_contract`
Machine boundary: Human-readable planning matrix. It does not prove resource
ownership, metering output, a billing event, price, account policy, or service
availability.

This matrix defines when Console manages or bills resources. `Service plan`
means a commercial offering; `OPL Package` means an owner-defined Agent or
capability Package carried by a native platform and aggregated by Framework.
The two are not interchangeable.

| Resource source | Example | Console management | Billing default |
| --- | --- | --- | --- |
| OPL Cloud-hosted | Managed Workspace, Agent Service, compute or storage | Yes | Service plan plus usage |
| Explicitly managed | Hospital, lab or enterprise resource under shared policy | Yes | Account or institution policy |
| User-provided local | User laptop or local runtime | Optional visibility | Not Cloud-billed by default |
| User-provided SSH/HPC | Lab server or HPC account | Optional visibility | Not Cloud-billed by default |
| External provider | Model, storage, compute or data source | Policy-managed when connected | Provider or pass-through usage |

## Metering Sources

- Gateway: provider, model, tokens, requests and cost signal.
- Workspace: service plan, uptime, storage allocation and users.
- Serve: service, revision, deployment, endpoint, invocation/session and consumer-principal refs.
- Compute: adapter, duration, GPU and queue signals.
- Storage: allocation, retention and transfer signals.
- Connector: provider action, request count and data-boundary signal.
- Agent: exact OPL Package ref, Agent Instance or Service Revision, run, resource profile and review gate.

## Balance And Settlement Owners

| Concern | Owner | Rule |
| --- | --- | --- |
| Spendable account balance | OPL Gateway | The only mutable wallet balance |
| Account-total billing projection and policy | OPL Control Plane | Owns the versioned price catalog and projects Gateway balance, AI usage, Workspace charges, refunds, and receipts without storing a second balance; Console presents its DTOs |
| Workspace monthly charge | OPL Control Plane | Owns one settlement operation per Workspace and billing period, debited from the Gateway balance; Console does not initiate a second settlement path |
| Provider/resource cost facts | OPL Fabric | Emits resource and provider facts; owns no wallet and keeps no balance |
| Charge and reconciliation evidence | OPL Ledger | Append-only receipts; never a spendable wallet |

Console can attribute usage to an OPL Package ref but cannot change its owner
descriptor/publication revision or the native carrier's installed state.
