# Latest Published Docs

Owner: `one-person-lab-cloud`
Purpose: `latest_published_docs_output_boundary`
State: `active_support`
Machine boundary: `docs/site/latest/` is local generated output for GitHub
Pages. It is not tracked on `main`.
Source truth stays in `docs/whitepapers/` and
`contracts/whitepaper_profile.json`. Artifact verification is generated beside
the ignored HTML/PDF bundle; publication receipts are GitHub Actions artifacts.

This repository builds the current Cloud whitepaper candidate. The Framework
repository owns formal publication of the five-document OPL family collection,
including the approved publication environment, the exact branded bundle, live
HTML/PDF verification, and publication receipts.

Generated output:

- `docs/site/latest/whitepapers/opl-cloud-whitepaper.html`
- `docs/site/latest/whitepapers/opl-cloud-whitepaper.pdf`
- `docs/site/latest/whitepapers/opl-cloud-whitepaper.verification.json`

Do not commit `docs/site/latest/` on `main`. Rebuild it with
`node --experimental-strip-types scripts/build-opl-cloud-whitepaper.ts`.
`bash scripts/publish-docs-latest.sh` does not build locally or write a Cloud
Pages branch. From clean, current Cloud `main`, it requests the Framework-owned
family publication workflow.
