# Instance Operations Boundary

OPL Cloud does not operate or automatically deploy a concrete instance. This
repository owns reusable source, contracts, installation assets, candidate
artifacts, and owner-admitted product releases.

- Generic Docker installation: [Install OPL Cloud](../installation.md)
- Product/instance ownership: [Architecture](../architecture.md)
- medopl.cn production operations: private
  `gaofeng21cn/opl-instance-medopl` repository

Production credentials, provider resources, approval gates, rollout, rollback,
and receipts must remain with the selected instance owner.

## Product Release Admission

A formal OPL Cloud Release records a candidate that has already passed the
required adoption path. During the current pre-1.0 phase it is not a CI
checkpoint, a record of every merged change, or an input used to discover
whether `opl-instance-medopl` can deploy the product.

### Release Unit

Before building a release candidate, the Product Release owner records one
reviewable release unit with all of the following:

- **Objective:** the complete product problem or bounded product milestone the
  Release closes;
- **Owner:** the primary Product module responsible for that outcome;
- **Acceptance:** the scoped Product checks plus the exact
  `opl-instance-medopl` deployment and product readback required before
  publication;
- **Artifact impact:** the runtime image, public contract, schema, dependency,
  installation asset, or security boundary that requires new released bytes;
- **Consumer:** during pre-1.0, the `opl-instance-medopl` candidate deployment;
- **Predecessor gap:** why the current Release cannot satisfy that consumer;
- **Known blockers:** confirmation that no known Product-side blocker remains
  for the same objective.

The release unit is review evidence attached to the owning change or release
conversation. It does not become a second roadmap, status, architecture, or
machine-contract owner.

### Candidate Qualification

PR checks, CI artifacts, and exact-SHA candidate images are replaceable inputs.
They may fail, be superseded, or be deleted without creating a formal version.
The candidate must identify one canonical Cloud SHA and one digest-addressed
multi-architecture image. It must be available to the Instance deployment path
without creating a Git tag, GitHub Release, or versioned GHCR tag.

`opl-instance-medopl` owns the protected deployment and qualification. It binds
the candidate SHA and digest, deploys through Instance `main`, performs the
required rollout and product acceptance, and returns an owner-authoritative
receipt. A failed or unknown deployment returns the release unit to development;
it does not create a Product Release.

### Publication

After candidate qualification succeeds, the repository owner verifies the
unused version, exact canonical SHA, candidate digest, required checks,
Instance receipt, release assets, checksums, and provenance. The owner then
explicitly dispatches the Release workflow from Cloud `main`. Publication must
promote the exact qualified image bytes and must fail if it would rebuild a
different digest.

The current workflow does not yet meet that sequence: one manual dispatch both
builds the OCI image and publishes the formal Release. Until a separate
deployable candidate path and exact-byte promotion are implemented, do not
publish a successor to `v0.1.7`.

The workflow remains create-only for ordinary publication and rejects reuse of
an existing tag, Release, or GHCR release tag. This prevents accidental
overwrite; it is not a ban on a separate, explicit repository-owner cleanup or
repair decision.

Do not publish a new Product Release solely for:

- documentation, status, plan, or test changes;
- CI, cache, runner, or build-performance changes;
- Instance workflow, environment, Secret, provider, or cluster configuration;
- deployment retries, failed qualification, account state, or approvals;
- an unclassified failure or an attempt to obtain more debugging evidence.

### Failure Classification

When candidate adoption or qualification fails, classify the owner before the
next attempt:

| Proven owner | Required action | Formal Product Release |
| --- | --- | --- |
| Instance configuration, workflow, provider, cluster, Secret, account, approval, or runtime data | Correct or retry in the Instance owner with the same candidate when its bytes remain valid | Not admitted |
| Unknown or conflicting evidence | Stop mutation and obtain authoritative readback for the exact candidate | Not admitted |
| Product runtime, public contract, schema, installation asset, dependency, or security boundary | Fix Cloud, create a new candidate SHA/digest, and repeat Instance qualification | Allowed only after the new candidate succeeds |

An urgent security correction may define a narrow release unit, but during the
current pre-1.0 phase it still requires exact candidate qualification before
formal publication.
