# Whitepapers

Owner: `one-person-lab-cloud`
Purpose: `whitepaper_source_root`
State: `active`
Machine boundary: Source prose and the artifact Profile for the public Cloud
whitepaper. Generated HTML/PDF/verification bundles live under ignored
`docs/site/latest/`. Publication truth comes from the approved bundle plus an
exact-byte public readback receipt, not from this directory or a successful
render alone.

This repository owns the prose and `contracts/whitepaper_profile.json`. OPL
Framework owns the only renderer, style and publication readback implementation.
`scripts/build-opl-cloud-whitepaper.ts` only locates Framework and calls its
`scripts/run-domain-whitepaper.ts` with the Cloud Profile.

A push to `main` builds a reviewable immutable bundle through the Framework
reusable workflow. Publishing is a separate manual dispatch with
`publish=true`; it uses the same bundle, requires the `whitepaper-production`
Environment, and closes only after exact-byte public readback.

Current source:

- `opl-cloud-whitepaper.md`
