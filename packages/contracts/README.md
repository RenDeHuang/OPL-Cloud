# OPL Cloud Contracts

Keep a machine contract only when two current owners or an external consumer
must agree on bytes or a stable public identity. Runtime behavior, internal
fields, retired alternatives, current progress, and implementation layout stay
in source, focused tests, or documentation.

## Current Contracts

| Contract | Current consumers |
| --- | --- |
| `opl-cloud-candidate-receipt-contract.json` | Candidate bundle tooling and the Candidate workflow |
| `opl-cloud-distribution-contract.json` | Candidate/Release validation and the instance handoff |
| `opl-cloud-fabric-launch-binding-contract.json` | Control Plane and Fabric stage request hashing |
| `opl-cloud-workspace-runtime-abi-contract.json` | Control Plane and Fabric Workspace WebUI routing |

The Candidate and Distribution contracts bind portable artifact identity; they
do not describe an instance deployment. Domains, provider selection, production
Secrets, deployment, rollback, qualification, and receipts belong to the
instance owner.

The Fabric launch contract contains only the hash encoding and golden vectors
that both Go modules consume. The Workspace Runtime ABI contains only the fixed
protocol and port projected by both modules.

## Admission

Add or retain a contract field only when:

- a current cross-module or external consumer reads it;
- the value has one authoritative owner;
- source or an existing public schema is not already the stronger owner; and
- removing deterministic enforcement would break a current compatibility,
  integrity, security, or irreversible-side-effect boundary.

Tests should exercise the consumer or public behavior. A test that only reads a
JSON file and repeats its fields does not justify the contract.
