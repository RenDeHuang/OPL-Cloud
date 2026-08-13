# Install OPL Cloud

OPL Cloud releases are standalone product artifacts. A release contains a
multi-architecture image in GHCR plus `compose.yaml`, an environment template,
and a machine-readable manifest. The product repository does not deploy any
concrete installation.

## Requirements

- Linux or macOS with Docker Engine and Docker Compose v2;
- an `amd64` or `arm64` host;
- a reachable Sub2API installation and its administrator credentials;
- a TLS reverse proxy when the Console is exposed beyond localhost.

## Start A Release

Download all five assets from one GitHub Release: `compose.yaml`,
`compose.local-workspace.yaml`, `opl-cloud.env.example`,
`opl-cloud-release.json`, and `SHA256SUMS`. Verify the downloaded bytes and
their GitHub-hosted provenance before trusting the manifest:

```bash
if command -v sha256sum >/dev/null; then
  sha256sum --check --strict SHA256SUMS
else
  shasum -a 256 -c SHA256SUMS
fi
for asset in compose.yaml compose.local-workspace.yaml opl-cloud.env.example opl-cloud-release.json SHA256SUMS; do
  gh attestation verify "$asset" \
    --repo gaofeng21cn/one-person-lab-cloud \
    --signer-workflow gaofeng21cn/one-person-lab-cloud/.github/workflows/release-opl-cloud-image.yml \
    --predicate-type https://github.com/gaofeng21cn/one-person-lab-cloud/attestations/opl-cloud-release/v1 \
    --deny-self-hosted-runners
done
```

The attestation predicate binds the signing workflow commit/ref, the separately
selected product SHA, release tag, immutable image digest, and checksum-manifest
digest. Verify that those predicate values and the manifest's `productSha`,
`releaseTag`, and immutable GHCR digest match the selected release before
preparing `.env` from the example. Release `v0.1.0` predates `SHA256SUMS` and artifact attestations;
use its recorded immutable image digest and source SHA as historical evidence,
not as proof of this newer provenance control.

```bash
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose --env-file .env ps
curl --fail http://127.0.0.1:8787/api/healthz
```

The Compose installation starts PostgreSQL, Ledger, Fabric, and Control Plane
as separate processes. Only the Control Plane is published to the host. Data is
stored in the `opl-cloud-postgres` named volume. The bundled PostgreSQL runtime
is pinned by its multi-architecture image digest; upgrades require an explicit
release change to that digest rather than a mutable tag update. First
initialization creates a separate database and role for each Go service. The environment template also
requires independent Control Plane, Fabric, and Ledger service tokens plus
independent Fabric and Ledger capability keys; Control Plane uses only the
target service's transport token and short-lived scoped capability for each
outbound call.

The services intentionally require either verified PostgreSQL TLS or an
explicit RFC1918 address when TLS is disabled. The Compose template therefore
places PostgreSQL at `OPL_POSTGRES_HOST` inside `OPL_DOCKER_SUBNET`. If the
default network overlaps another Docker or host network, choose an unused
RFC1918 subnet and update both values together before startup.

On first start, Control Plane binds its reserved operator account to the active
Sub2API administrator identity returned for `OPL_SUB2API_ADMIN_EMAIL`. The
email is installation-owned rather than product-coded; a later ID or email
mismatch fails closed instead of silently changing operator authority.

This is not yet a complete Workspace installation. Fabric includes a real
`local-docker` provider, but the portable Compose profile leaves Workspace launch
workers disabled, does not grant Fabric access to a Docker authority, and does
not select an immutable OPL App/WebUI Workspace image. A healthy Compose stack
therefore proves only that the Cloud control services start; it cannot yet create,
read back, access, or delete a Workspace. See [current capability](status.md) and
the [P0 gap](roadmap.md).

## Upgrade And Rollback

Set `OPL_CLOUD_IMAGE` to the immutable digest from another release manifest,
then run `docker compose pull` and `docker compose up -d`. Rollback uses the
same procedure with the previous digest. Mutable `latest` and `stable` tags are
not published.

## Provider Boundary

The current source includes both the default local-Docker adapter and the
explicit Tencent TKE adapter, but the portable Compose profile intentionally
starts control services only. Console, Control Plane, Fabric, Ledger,
persistence, and health checks can run on any supported Docker host. Workspace
delivery remains disabled until an installation profile supplies the selected
provider authority and an immutable Workspace image; managed providers also need
their credentials and network/storage settings. A successful Compose health
check is not evidence that any provider-backed Workspace delivery is ready.

`opl-instance-medopl` is the separate private instance owner for medopl.cn. It
selects Tencent/TKE, owns production Secrets and deployment workflows, pins an
immutable OPL Cloud release, and records deployment/rollback receipts.
