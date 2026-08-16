# Tencent TKE Instance Deployment Boundary

Tencent TKE is a supported Fabric adapter, not the OPL Cloud product boundary.
Cloud retains the reusable provider adapter, provider-neutral contracts, and
portable product runtime/release assets. It does not own a TKE cluster,
production environment, Secrets, deployment dispatch, rollback, or runtime
receipt.

The medopl.cn TKE profile, production workflow, and instance-specific
production, acceptance, recovery, canary, rollback, and evidence tools are
owned only by the private `gaofeng21cn/opl-instance-medopl` repository. Its
workflows consume an immutable OPL Cloud candidate identified by product SHA
and image digest, while running those tools from the Instance checkout.

The source cutover is complete in the repositories: Cloud no longer tracks the
retired instance tool entrypoints or their focused tests, and no accepted
workflow caller requires those Cloud paths. This document does not authorize a
production operation; deployment and runtime claims remain Instance-owner
evidence.

For a generic Docker installation, use [Install OPL Cloud](../installation.md).
