# OPL Cloud Documentation

This index owns the documentation hierarchy. It follows OPL Doc's semantic
governance model: one current owner per topic, current truth separated from
active gaps, support detail and history.

## Hierarchy

| Level | Question | Canonical owner | Change rate |
| --- | --- | --- | --- |
| 1. Product concept | Why does OPL Cloud exist and what is in scope? | [project.md](./project.md) and the [whitepaper](./whitepapers/opl-cloud-whitepaper.md) | Long term |
| 2. Target architecture | What should the product become and who owns each authority? | [architecture.md](./architecture.md) and durable [decisions.md](./decisions.md) | Long term |
| 3. Durable invariants | Which safety, integrity and ownership facts must survive refactors? | [invariants.md](./invariants.md) and eligible machine contracts | Infrequent |
| Security policy | Which boundaries are supported and how are vulnerabilities reported? | [`SECURITY.md`](../SECURITY.md) | Infrequent |
| 4. Current implementation | What paths, schemas and module boundaries exist now? | [implementation-architecture.md](./implementation-architecture.md), source, schemas and focused tests | Frequent |
| 5. Functional modules | What user capability does each product surface provide? | `docs/opl-*.md`, `docs/product/**` and public API schemas | Feature paced |
| 6. Status and plan | What is proven now, what is missing and what comes next? | [status.md](./status.md) for evidence; [roadmap.md](./roadmap.md) for gaps, priority and acceptance | Continuous |
| 7. Operations | How is the current release operated? | `docs/runtime/**`, deployment manifests and workflows | Release paced |
| 8. History | Why did a retired decision or shape exist? | [history](./history/README.md) | Append or retire |

The hierarchy is directional. A lower level may implement, explain or report
an upper-level decision; it cannot redefine it. When a product or architecture
owner changes, the same change must reconcile affected invariants, current
implementation docs, module docs, status and roadmap. A lower projection that
cannot yet follow is an explicit roadmap gap, not a competing SSOT.

## Authority Rules

- Target intent comes from the latest user decision and its canonical product
  or architecture owner. Current code does not silently redefine the target.
- Current implementation claims require source, schema, tests or runtime
  evidence. A target document or contract field is not delivery evidence.
- `docs/status.md` is a replaceable current snapshot, not a chronological
  ledger. `docs/roadmap.md` owns open gaps and acceptance outcomes, not agent
  prompts, shell commands or branch write sets.
- Machine contracts exist only when a cross-module, public-interface, security,
  data-integrity or irreversible-side-effect fact needs deterministic
  enforcement. Visual preference and ordinary implementation shape stay out.
- `SECURITY.md` owns disclosure scope and reporting policy. Scanner artifacts,
  GitHub alerts, Issues, pull requests, and agent sessions are evidence or work
  surfaces; they do not become a second security, product, or status owner.
- `one-person-lab` owns the reusable development method. Instance identity,
  provider profile and deployment receipts belong to the instance repository;
  `opl-cloud` remains an internal artifact identifier.

## Active Navigation

- [Console Workspace product](./product/console-workspace-v1.md)
- [Console experience guide](./product/console-experience-guide.md)
- [Workspace identity and external SaaS boundary](./workspace-identity-and-external-saas-boundary.md)
- [Production runbook](./runtime/production-runbook.md)
- [TKE deployment](./runtime/tke-production-deployment.md)
- [Documentation lifecycle policy](./policies/docs-lifecycle-policy.md)
- [Development worktree policy](./policies/development-worktree-policy.md)

## Lifecycle

Classify sections by meaning, not merely by filename:

- `current_truth`: concise content owned here and supported by the proper source;
- `active_gap`: only in `docs/roadmap.md`;
- `support_detail`: explanation that points to, but does not duplicate, its owner;
- `history_or_provenance`: under `docs/history/**` and never a current gate;
- `stale_or_conflicting`: remove or reconcile in the same change.

Do not test prose wording. Test the owned behavior or schema. Dated plans,
design freezes, screenshots, closeout notes and completed implementation
ledgers belong in history or Git history, not active documentation.
