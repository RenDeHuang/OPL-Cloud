# Status

## Current Boundary

Current status is a controlled CPU Workspace pilot, not public GA.

Implemented:

- owner/admin login and server-side tenant isolation;
- live-verified positive `sub2apiUserId` account mapping before user creation;
- live Sub2API USD balance in Console;
- Basic monthly compute purchase;
- storage purchase in 10 GB blocks;
- stable purchase/renewal redeem codes and recovery states;
- renewal, auto-renew control, expiration, retained storage, and entitlement gates;
- dedicated Tencent CVM, CBS, attachment, runtime, and Workspace URL flows;
- Ledger billing receipts and product-scoped receipt lookup;
- separate PostgreSQL-backed Control Plane, Fabric, and Ledger services;
- TKE deployment workflow and a legacy production-verifier implementation.

Not ready:

- Pro purchase and provider evidence, although its `8c16g`, CNY 1,500 compute,
  and CNY 180/100GB storage definition is approved;
- debit-before-provider ordering, confirmed-absence refund, and ambiguous-result
  manual review for monthly resource settlement;
- prepaid Tencent CVM/CBS procurement and renewal;
- account-owned `opl-workspace` Key projection and Fabric Secret injection;
- immutable deployment of the verified `one-person-lab-webui:26.7.13` source digest;
- the reusable `SA5.MEDIUM4` plus 10GB CBS Verification Slot;
- safe refund/manual-review verification; the legacy paid verifier is blocked and is not a release gate;
- GPU packages;
- public self-registration or a reusable unified identity system;
- production backup/restore, because the current TKE snapshot installation does
  not expose the required GA `snapshot.storage.k8s.io/v1` API;
- public GA operational evidence.

Sub2API source, deployment, API keys, routing, request usage, and balance remain
outside this repository. Console may read the signed-in account's single active
`opl-workspace` Key and Key DTO usage on demand, but never mirrors them.

## Completion Gate

```bash
npm test
npm run typecheck
npm run build
(cd services/control-plane && go test ./...)
(cd services/fabric && go test ./...)
(cd services/ledger && go test ./...)
git diff --check
```

Production delivery additionally requires CI, immutable image publication,
bounded rollout status for all three services, recovery evidence, and the
reusable verification receipt defined by `docs/invariants.md`.
