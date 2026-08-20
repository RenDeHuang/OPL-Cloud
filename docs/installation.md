# Install OPL Cloud

OPL Cloud Releases are standalone product artifacts. A Release contains a
multi-architecture image in GHCR plus Compose files, an environment template,
checksums, and a machine-readable manifest. The product repository does not
deploy any concrete installation.

The only current public Release is `v0.1.7`, built from product SHA
`a59bde68397528186a5220f73195fa1f3eda311b` with GHCR index digest
`sha256:e64504731f8b61c0864cf59faa647a1150e8a2a5eada34b26faf3a5487d28e8f`.
Historical `v0.1.0` through `v0.1.6` Releases, tags, and GHCR objects were
removed and must not be used as installation or rollback targets.

## Requirements

- Linux 5.14+ with Docker Engine and Docker Compose v2 when using the
  `local-docker` Workspace provider. The local provider requires a dedicated
  ext4/XFS mount with project quota enabled; macOS and Docker Desktop are not
  supported for Local-Docker Workspace launches. macOS may run the Cloud
  control services only when an instance selects another provider;
- an `amd64` or `arm64` host;
- a reachable Sub2API installation; `platform_owned` and `managed_tke` require
  administrator credentials, while `customer_owned` requires one ordinary
  Sub2API user account;
- a TLS reverse proxy when the Console is exposed beyond localhost.

## Start A Release

Download the Compose base, the three deployment overlays, the two Fabric
overlays, the local workspace overlay, `opl-cloud.env.example`,
`opl-cloud-release.json`, and `SHA256SUMS` from Release `v0.1.7`. Verify the downloaded bytes and
their GitHub-hosted provenance before trusting the manifest:

```bash
if command -v sha256sum >/dev/null; then
  sha256sum --check --strict SHA256SUMS
else
  shasum -a 256 -c SHA256SUMS
fi
for asset in compose.yaml compose.deployment-platform-owned.yaml compose.deployment-managed-tke.yaml compose.deployment-customer-owned.yaml compose.fabric-local-docker.yaml compose.fabric-tencent-tke.yaml compose.local-workspace.yaml opl-cloud.env.example opl-cloud-release.json SHA256SUMS; do
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
preparing `.env` from the example. For the current Release, the required values
are `v0.1.7`, product SHA
`a59bde68397528186a5220f73195fa1f3eda311b`, and image digest
`sha256:e64504731f8b61c0864cf59faa647a1150e8a2a5eada34b26faf3a5487d28e8f`.

```bash
docker compose --env-file .env -f compose.yaml \
  -f compose.deployment-customer-owned.yaml \
  -f compose.fabric-local-docker.yaml \
  -f compose.local-workspace.yaml pull
docker compose --env-file .env -f compose.yaml \
  -f compose.deployment-customer-owned.yaml \
  -f compose.fabric-local-docker.yaml \
  -f compose.local-workspace.yaml up -d
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

On first start, Control Plane binds its Cloud identity to the configured
Sub2API identity. In `platform_owned` and `managed_tke`, that identity is an
administrator and owns resource billing. In `customer_owned`, it is an ordinary
owner account; compute and storage are customer-owned and the Cloud resource
quote and debit are zero.

The deployment and Fabric choices are independent. Select exactly one deployment
overlay (`platform_owned`, `managed_tke`, or `customer_owned`) and exactly one
Fabric overlay (`local-docker` or `tencent-tke`). The local workspace overlay
adds the customer-owned PostgreSQL bind and enables Workspace launch; the
Fabric overlay supplies only the provider authority and provider-specific
mounts/credentials. `local-docker` Workspace launches require the Linux
project-quota host described above. A root from the schema-1 directory layout,
a non-Linux host, Docker Desktop, a bind-mounted subdirectory, or a filesystem
without project quota fails readiness before any Launch mutation; existing
schema-1 Workspaces must be deleted and recreated with the preceding release.

## Upgrade And Rollback

Set `OPL_CLOUD_IMAGE` to the immutable digest from an admitted Release manifest,
then run `docker compose pull` and `docker compose up -d`. Rollback uses the
same procedure with another currently available admitted digest. At present,
only `v0.1.7` is available, so there is no earlier public Release rollback
target. Mutable `latest` and `stable` tags are not published.

## Provider Boundary

The current source includes both a customer-Docker adapter and an explicit
Tencent TKE adapter. The provider overlay owns its authority, storage/network
settings, and display label; the deployment overlay owns only account mode and
billing policy. A successful Compose health check is not evidence that any
provider-backed Workspace delivery is ready until the selected workspace
profile's provider health check passes.

`opl-instance-medopl` is the separate private medopl instance owner. It
explicitly supplies the `.com` domains, Tencent/TKE Provider Profile, immutable
Workspace image, production Secrets and deployment workflows, deploys and
qualifies an exact pre-1.0 Cloud Candidate before formal publication, and
records deployment/rollback receipts. An allowlisted Cloud Release publisher,
either the repository owner or `RenDeHuang`, may publish a successor Release
only after the same Candidate passes the supported local path and Instance
qualification, by promoting the qualified Cloud image bytes without rebuild;
the current workflow does not yet provide this path.
