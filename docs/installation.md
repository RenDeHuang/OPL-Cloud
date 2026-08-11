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

Download `compose.yaml`, `opl-cloud.env.example`, and
`opl-cloud-release.json` from one GitHub Release. Verify that the manifest's
`productSha`, `releaseTag`, and immutable GHCR digest match the selected
release, then prepare `.env` from the example.

```bash
docker compose --env-file .env pull
docker compose --env-file .env up -d
docker compose --env-file .env ps
curl --fail http://127.0.0.1:8787/api/healthz
```

The Compose installation starts PostgreSQL, Ledger, Fabric, and Control Plane
as separate processes. Only the Control Plane is published to the host. Data is
stored in the `opl-cloud-postgres` named volume.

This is not yet a complete Workspace installation. The repository currently
has no `local-docker` Workspace provider, and the Compose profile leaves
Workspace launch workers disabled. A healthy Compose stack proves only that the
Cloud control services start; it cannot create, read back, access, or delete an
OPL App/WebUI Workspace. See [current capability](status.md) and the
[P0 gap](roadmap.md).

## Upgrade And Rollback

Set `OPL_CLOUD_IMAGE` to the immutable digest from another release manifest,
then run `docker compose pull` and `docker compose up -d`. Rollback uses the
same procedure with the previous digest. Mutable `latest` and `stable` tags are
not published.

## Provider Boundary

The current source includes the Tencent TKE Fabric adapter, but the portable
Compose profile intentionally does not select or configure an infrastructure
provider. Console, Control Plane, Fabric, Ledger, persistence, and health checks
can run on any supported Docker host. Workspace procurement and delivery remain
disabled until an instance supplies a supported provider profile, credentials,
network/storage settings, and an immutable Workspace image. A successful
Compose health check is not evidence that local Docker or any other
provider-backed Workspace delivery is ready.

`opl-instance-medopl` is the separate private instance owner for medopl.cn. It
selects Tencent/TKE, owns production Secrets and deployment workflows, pins an
immutable OPL Cloud release, and records deployment/rollback receipts.
