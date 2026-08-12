# OPL Cloud Security Policy

This policy owns the supported security boundary and private reporting route for
`one-person-lab-cloud`. Architecture decisions remain in
[`docs/architecture.md`](docs/architecture.md), durable safety properties in
[`docs/invariants.md`](docs/invariants.md), current evidence in
[`docs/status.md`](docs/status.md), and unresolved work in
[`docs/roadmap.md`](docs/roadmap.md).

## Supported Scope

Security reports are in scope when they affect the current default branch or a
published OPL Cloud release and cross one of these supported boundaries:

- an anonymous browser, authenticated tenant, or less-privileged service
  influencing Control Plane, Fabric, Ledger, Console, or provider behavior;
- one tenant or service identity reading or mutating another tenant's data,
  Workspace, provider resource, job, receipt, or credential;
- untrusted repository code, dependencies, images, or downloaded tools reaching
  GitHub publication credentials or release artifacts;
- Cloud release consumers receiving mutable, substituted, or unverifiable
  artifacts; or
- attacker-controlled input causing unbounded resource use on a supported
  service path.

Cloud owns reusable source, contracts, portable images, GitHub Releases, and
release integrity. Instance repositories own provider selection, production
Secrets, deployment authorization, runtime verification, rollback, and receipts.
A Cloud report should not include or require access to an instance's private
network or production credentials.

Local developer commands, tests, fixtures, examples, explicitly trusted
operator configuration, historical migrations, and archived provenance are not
automatically security boundaries. They remain in scope when evidence shows
that a less-trusted actor can reach them through a supported product or release
path. Generated code is assessed through its handwritten callers and resulting
runtime behavior unless the generated output itself introduces the boundary.

## Security Properties

We treat these properties as security-sensitive:

- authentication and authorization are checked against the final account,
  Workspace, resource, operation, and persisted owner;
- service credentials convey only the identity and capabilities required by the
  caller, and provider mutations fail closed on ambiguous authority;
- images and releases are both immutable and from an approved owner; a digest
  proves content identity but does not by itself authorize its source;
- untrusted build or dependency code cannot access publication credentials;
- Secrets never enter repository content, logs, command arguments, caches, or
  release assets; and
- request, response, collection, and rate-limit state growth is bounded on
  supported untrusted-input paths.

## Reporting A Vulnerability

Use GitHub's **Report a vulnerability** action on this repository's Security
page. Private vulnerability reporting is the canonical disclosure route. Do not
open a public issue, pull request, or discussion for a suspected vulnerability.

Include the affected revision or release, impacted component and boundary,
required attacker access, reproduction or source-to-sink evidence, expected
impact, and any known mitigation. Remove customer data, access tokens, signed
URLs, private endpoints, provider responses, and exploit material that is not
needed to validate the report.

We will acknowledge the report in the private advisory, reproduce or statically
validate the claim, establish severity and ownership, and coordinate a fix and
disclosure when the evidence supports one. A scanner alert or report is a lead;
it is not treated as a confirmed vulnerability or a completed fix without
boundary-specific triage and validation.

## Known Limitations

Security scanning is risk-based and partial. CodeQL, Dependabot, secret
scanning, dependency review, and repository reviews cover different failure
modes and may produce false positives or miss reachable flaws. Passing checks
does not prove release or production safety. Current open evidence and planned
hardening are reported in [`docs/status.md`](docs/status.md) and
[`docs/roadmap.md`](docs/roadmap.md); sensitive exploit detail remains in the
private reporting or owner-authorized tracking surface.
