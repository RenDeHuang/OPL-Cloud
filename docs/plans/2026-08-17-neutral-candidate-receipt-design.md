# Neutral Candidate Receipt Design

## Objective

Create a non-Release Cloud candidate channel that binds one exact canonical
Cloud source revision to one immutable `linux/amd64` Cloud image and one
caller-selected immutable Workspace image. The output is a neutral candidate
receipt consumed by Instance workflows without carrying J2, deployment,
provider, account, or environment semantics.

## Ownership

- `one-person-lab-cloud` owns the candidate schema, canonicalization,
  validation, Cloud image build, GHCR publication, and candidate artifact.
- `opl-instance-medopl` owns transport decoding, protected registry readback,
  deployment, rollback, Fresh admission/readback, and Instance receipts.
- The Instance must not copy the Cloud candidate schema. It performs a minimal
  transport-envelope parse, checks out the receipt's exact Product SHA, and
  invokes the Cloud-owned validator from that checkout.

## Candidate Shape

The canonical JSON object has exact keys and schema version 1:

```json
{
  "schemaVersion": 1,
  "kind": "opl_cloud_candidate",
  "product": {
    "repository": "gaofeng21cn/one-person-lab-cloud",
    "sha": "<40 lowercase hex>",
    "tree": "<40 lowercase hex>"
  },
  "platform": "linux/amd64",
  "cloudImage": {
    "repository": "ghcr.io/gaofeng21cn/one-person-lab-cloud",
    "ref": "<repository>@sha256:<digest>",
    "digest": "sha256:<digest>",
    "revision": "<same product SHA>"
  },
  "workspaceImage": {
    "ref": "<repository>@sha256:<digest>",
    "digest": "sha256:<digest>"
  },
  "provenance": {
    "workflowRepository": "gaofeng21cn/one-person-lab-cloud",
    "workflowSha": "<40 lowercase hex>",
    "workflowRunId": "<positive decimal>",
    "workflowRunAttempt": "<positive decimal>"
  }
}
```

The receipt digest is the SHA-256 of canonical JSON bytes. It is transported
separately and referenced by every deployment, admission, and readback receipt.

## Candidate Workflow

`.github/workflows/build-opl-cloud-candidate.yml` is a Cloud product workflow,
not an Instance business-scenario entry. It accepts:

- `product_sha`: exact commit already in Cloud `main` history;
- `workspace_image`: immutable `repository@sha256` selected for the candidate.

It verifies source ancestry and tree, builds and pushes one `linux/amd64` Cloud
image with OCI revision equal to `product_sha`, reads the exact GHCR digest and
platform back, writes the canonical receipt, validates it with the Cloud-owned
tool, and uploads it as a short-lived artifact. It creates no Git tag, GitHub
Release, semantic version, Instance deployment, or Workspace mutation.

The Workspace reference is bound syntactically by Cloud. Provider/private
registry existence, platform, and pullability remain Instance readback because
Cloud must not acquire Instance registry credentials.

## Release Relationship

The candidate image is pushed once under a run-scoped candidate tag and then
addressed only by digest. A later formal Release may promote the same digest
without rebuilding. Exact-byte Release promotion is a separate roadmap item;
this change creates the prerequisite candidate channel and receipt.

## Failure Semantics

- Invalid or non-main Product SHA fails before registry mutation.
- Mutable or malformed Workspace references fail before registry mutation.
- Build, push, digest, platform, revision, or schema mismatch fails closed.
- A failed candidate run creates no formal version or Release.
- Receipts contain no credentials, Instance domains, provider IDs, account
  identity, operation identity, or environment configuration.

## Verification

Focused tests cover exact schema keys, canonical digest stability, source/tree
binding, immutable image rules, workflow owner authority, `linux/amd64`, OCI
revision, GHCR readback, absence of Release publication, and sensitive-field
rejection. The aggregate gate is `npm run verify:local:full`.

## Documentation Lifecycle

This dated design is an active implementation aid only. Before final merge,
durable ownership and behavior move to the distribution contract,
`docs/decisions.md`, `docs/implementation-architecture.md`, `docs/status.md`,
and `docs/roadmap.md`; this file is then deleted from the active tree and
retained through Git/PR history.
