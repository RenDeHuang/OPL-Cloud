# Cloud Repository Unification, 2026-08-11

State: `migration_record`

The OPL Cloud product and implementation were consolidated into the transferred
implementation repository because that GitHub repository owns the material pull
request, Actions, Environment and production deployment history.

## Identity

- Retained GitHub repository id: `1285199013`
- Retained repository before final rename: `gaofeng21cn/opl-cloud`
- Retained baseline main: `c2b47d23abfd964c5211b025346f96541c8d3cf2`
- Documentation source repository id: `1285709904`
- Documentation source baseline main: `43830f7bd209be293a1ce6445202a429b6996cda`
- Documentation source Pages baseline: `97122721f1389cb6db46cf76b25484614e8d5278`
- Canonical repository after migration: `gaofeng21cn/one-person-lab-cloud`
- Documentation archive after migration: `gaofeng21cn/one-person-lab-cloud-docs-archive`

## Boundary

The unified repository owns product architecture, whitepaper, roadmap, Console,
Control Plane, Fabric, Ledger, Workspace delivery, machine contracts and
reusable release mechanisms. `opl-cloud` remains an internal artifact and
service identifier. `opl-instance-medopl` remains the target owner for concrete
medopl configuration and deployment evidence.

The documentation archive preserves history only. It is not a current product,
planning, implementation or Pages writer.

## Recovery

Pre-migration mirrors and verified all-ref bundles are stored outside both
repositories at:

`/Users/gaofeng/Documents/Codex/2026-08-11/opl-cloud-unification-backup`

Git bundles do not contain GitHub Secrets, Environment settings, runner
registration or external provider policy. Those surfaces require independent
post-rename readback.
