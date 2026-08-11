# Research Pattern Provenance

Owner: `one-person-lab-cloud`
Purpose: `external_research_provenance`
State: `history_reference`
Machine boundary: Historical design provenance only. It does not define the
current Cloud product split, endorse an external implementation, or prove any
capability is implemented or ready.

Claude Science was a useful reference because it framed scientific AI as a
workbench: domain skills, data connections, compute resources, software
environments, reproducible artifacts, and reviewer agents work together around
research outputs.

OPL Cloud retained the platform lesson rather than the life-sciences packaging.
Current authority and product language are owned by
[the architecture](../architecture.md) and focused product references.

## OPL Translation At Intake

| Reference pattern | OPL Cloud target surface |
| --- | --- |
| Scientific workbench | OPL App / OPL Workspace |
| Team and admin enablement | OPL Console |
| Model/provider access | OPL Gateway |
| HPC, SSH, local, and on-demand compute | OPL Fabric / OPL Compute |
| Scientific databases and connectors | OPL Fabric / OPL Connect |
| Domain skills and reference packs | Domain owner packages installed through OPL Packages |
| Exact code, environment, and history | OPL Environments + OPL Ledger refs |
| Reviewer agent | Domain reviewer gate with Ledger refs |
| Figures and manuscripts | App / Workspace artifact delivery surface |

## Lessons Retained

1. Cloud should support a continuous workbench, not only a connector catalog.
2. Meaningful remote work should return inspectable refs and receipts.
3. Artifacts should remain connected to inputs, code/actions, environment,
   owner, reviewer checks, and continuation.
4. Sensitive data should remain with the user or institution by default.
5. Domain owners keep professional truth; OPL Packages owns package lifecycle;
   Connect stabilizes shared access; Ledger records refs.

The current minimum receipt and review shapes live in
[the Ledger target](../opl-ledger.md) and
[receipt planning schema](../contracts/ledger-receipt-schema.md), not in this
historical reference.
