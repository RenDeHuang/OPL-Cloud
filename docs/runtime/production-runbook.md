# Instance Operations Boundary

OPL Cloud does not operate or automatically deploy a concrete instance. This
repository publishes reusable source, contracts, installation assets, and
versioned create-only releases only.

- Generic Docker installation: [Install OPL Cloud](../installation.md)
- Product/instance ownership: [Architecture](../architecture.md)
- medopl.cn production operations: private
  `gaofeng21cn/opl-instance-medopl` repository

Production credentials, provider resources, approval gates, rollout, rollback,
and receipts must remain with the selected instance owner.

## Product Release Admission

A formal OPL Cloud Release transfers one portable product artifact that Product
owners treat as immutable after publication to a prepared consumer. It is not a
CI checkpoint, a record of every merged change, or a way to diagnose an
Instance.

### Release Unit

Before dispatching the release workflow, the Product Release owner must record
one reviewable release unit with all of the following:

- **Objective:** the complete product problem or bounded product milestone the
  Release closes;
- **Owner:** the primary Product module responsible for that outcome;
- **Acceptance:** the scoped criteria and exact source, test, contract, and
  artifact evidence proving the Product side is ready;
- **Artifact impact:** the runtime image, public contract, schema, dependency,
  installation asset, or security boundary that requires new released bytes;
- **Consumer:** the Instance or installer prepared to adopt the Release after
  publication;
- **Predecessor gap:** why the current Release cannot satisfy that consumer;
- **Known blockers:** confirmation that no known Product-side blocker remains
  for the same objective.

The release unit is review evidence attached to the owning change or release
conversation. It does not become a second roadmap, status, architecture, or
machine-contract owner.

### Candidate And Ready

PR checks, CI artifacts, and exact-SHA candidate images are replaceable
qualification inputs. They may fail, be superseded, or be rebuilt without
creating a formal version. The Product owner may issue a `READY` receipt only
when the release unit's scoped acceptance criteria pass, the exact candidate
SHA and tree are fixed, and no known Product-side blocker for that unit remains.

`READY` proves Product readiness only. It does not prove an Instance deployment
or production outcome. Production qualification remains the Instance owner's
responsibility and binds the exact released SHA and image digest.

### Publication And Handoff

After `READY`, the Product Release owner verifies the unused version, exact
canonical SHA, required checks, release assets, digest-bound image identity,
checksums, provenance, and prepared consumer. One formal Release is then
published for the release unit and handed to the consumer. The consumer reuses
that same Release for deployment, configuration correction, retry, rollback,
and qualification.

Product policy requires published tags, Releases, and image identities to
remain fixed, and the create-only workflow rejects reuse of an existing tag,
Release, or GHCR release tag. That does not prove platform-enforced
immutability. Report GitHub's immutable-release setting, Release API state, and
tag protection separately in `docs/status.md`; keep any missing enforcement or
accepted residual risk in `docs/roadmap.md`.

Do not publish a new Product Release solely for:

- documentation, status, plan, or test changes;
- CI, cache, runner, or build-performance changes;
- Instance workflow, environment, Secret, provider, or cluster configuration;
- deployment retries, account state, approvals, or production evidence;
- an unclassified failure or an attempt to obtain more debugging evidence.

### Failure Classification

When adoption or qualification fails, keep the released product SHA and digest
fixed while the owning boundary is identified:

| Proven owner | Required action | New Product Release |
| --- | --- | --- |
| Instance configuration, workflow, provider, cluster, Secret, account, approval, or runtime data | Correct or retry in the Instance owner and reuse the same Release | Forbidden |
| Unknown or conflicting evidence | Stop mutation, preserve the same Release, and obtain authoritative readback | Forbidden |
| Product runtime, public contract, schema, installation asset, dependency, or released security boundary | Reopen the Product lane, qualify a new candidate, and obtain a new `READY` receipt | Allowed after qualification |

An urgent security correction follows the same ownership and evidence rules but
may define a narrowly scoped release unit without waiting for an unrelated
product milestone.
