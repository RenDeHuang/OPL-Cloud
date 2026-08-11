# Latest Published Docs

Owner: `one-person-lab-cloud`
Purpose: `latest_published_docs_output_boundary`
State: `active_support`
Machine boundary: `docs/site/latest/` is local generated output for GitHub
Pages. It is not tracked on `main`.
Source truth stays in `docs/whitepapers/` and
`contracts/whitepaper_profile.json`. Artifact verification is generated beside
the ignored HTML/PDF bundle; publication receipts are GitHub Actions artifacts.

This repo publishes one current public whitepaper copy, not one copy per
release. A normal push builds a reviewable immutable bundle. An explicit
`workflow_dispatch` with `publish=true` enters the `whitepaper-production`
Environment, deploys the same bundle without rebuilding, and verifies the live
HTML/PDF bytes before writing a publication receipt.

Generated output:

- `docs/site/latest/whitepapers/opl-cloud-whitepaper.html`
- `docs/site/latest/whitepapers/opl-cloud-whitepaper.pdf`
- `docs/site/latest/whitepapers/opl-cloud-whitepaper.verification.json`

Do not commit `docs/site/latest/` on `main`. Rebuild it with
`node --experimental-strip-types scripts/build-opl-cloud-whitepaper.ts`.
`bash scripts/publish-docs-latest.sh` no longer builds locally or force-pushes
an orphan branch; it requests the approved remote workflow from clean, current
`main`.
