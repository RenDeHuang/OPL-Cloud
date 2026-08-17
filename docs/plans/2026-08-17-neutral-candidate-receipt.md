# Neutral Candidate Receipt Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a non-Release `linux/amd64` Cloud candidate from an exact canonical Product SHA and emit one neutral immutable candidate receipt.

**Architecture:** Cloud owns the candidate schema, validator, GHCR candidate build, and artifact. Instance consumers use the Cloud validator from the exact Product checkout and never copy this contract. The workflow publishes a run-scoped candidate tag, records the registry digest, and performs no formal Release action.

**Tech Stack:** GitHub Actions, Node.js 24 TypeScript, Docker Buildx, GHCR, JSON contracts, Node test runner.

---

### Task 1: Lock the candidate receipt contract

**Files:**
- Create: `packages/contracts/opl-cloud-candidate-receipt-contract.json`
- Create: `tests/contracts/product-candidate-receipt.test.ts`
- Modify: `packages/contracts/opl-cloud-distribution-contract.json`

1. Write a failing contract test requiring schema version 1, exact Product
   repository/SHA/tree, `linux/amd64`, immutable Cloud and Workspace refs,
   matching Cloud revision, provenance, and no Instance/J2 fields.
2. Run `node --test tests/contracts/product-candidate-receipt.test.ts` and
   confirm it fails because the contract is absent.
3. Add the contract and distribution-owner projection.
4. Re-run the focused test and commit.

### Task 2: Implement canonical receipt validation

**Files:**
- Create: `tools/cloud-candidate-receipt.ts`
- Create: `tests/tools/cloud-candidate-receipt.test.ts`

1. Write failing tests for exact keys, canonical ordering, digest stability,
   base64 round-trip, source/image mismatch, forbidden keys, and mode 0600
   output.
2. Run `node --test tests/tools/cloud-candidate-receipt.test.ts` and confirm the
   missing module failure.
3. Implement exported `canonicalJson`, `validateCloudCandidateReceipt`,
   `candidateReceiptDigest`, `decodeCandidateReceipt`, and CLI commands
   `validate`, `digest`, and `export-env`.
4. Reject unknown keys, non-canonical references, J2/J4/J5 terminology, Secret
   fields, Instance domains, account/operation/provider identities, and any
   platform except `linux/amd64`.
5. Re-run focused tests and commit.

### Task 3: Add the non-Release Candidate workflow

**Files:**
- Create: `.github/workflows/build-opl-cloud-candidate.yml`
- Modify: `tools/validate-product-boundary.mjs`
- Modify: `tests/contracts/product-distribution.test.ts`
- Create: `tests/contracts/product-candidate-workflow.test.ts`

1. Write failing tests requiring exact `product_sha` and immutable
   `workspace_image` inputs, owner-only dispatch from `main`, exact source
   ancestry/tree verification before registry access, one `linux/amd64` build,
   OCI revision label, GHCR digest/platform readback, canonical receipt upload,
   and zero Release/tag/Instance actions.
2. Run the two focused test files and confirm failure.
3. Implement the workflow with `packages: write`, a run-scoped candidate tag,
   `docker buildx build --platform linux/amd64 --push`, metadata/readback, and a
   short-lived `opl-cloud-candidate-<product_sha>` artifact.
4. Update product-boundary validation to admit the Cloud candidate workflow but
   reject Instance concerns and formal publication commands in it.
5. Re-run focused tests and commit.

### Task 4: Reconcile canonical documentation

**Files:**
- Modify: `docs/decisions.md`
- Modify: `docs/implementation-architecture.md`
- Modify: `docs/status.md`
- Modify: `docs/roadmap.md`

1. Record that non-Release candidates are built from exact canonical SHAs and
   consumed by Instance before publication.
2. Mark candidate-channel creation implemented while retaining exact-byte
   formal promotion as an explicit open gap.
3. Ensure no document claims local Docker or a local digest is production
   candidate authority.
4. Run focused documentation/contract tests and commit.

### Task 5: Run Cloud aggregate gates

1. Run `npm test`.
2. Run `npm run verify:local:full`.
3. Run `git diff --check`.
4. Review the workflow for Release, deployment, Secret, and Instance leaks.

### Task 6: Retire construction documents

**Files:**
- Delete: `docs/plans/2026-08-17-neutral-candidate-receipt-design.md`
- Delete: `docs/plans/2026-08-17-neutral-candidate-receipt.md`

1. Confirm every durable decision is present in a canonical owner.
2. Delete these dated construction files before final merge.
3. Re-run `git diff --check` and the documentation boundary tests.
