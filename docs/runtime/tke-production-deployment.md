# Tencent TKE Instance Deployment

Tencent TKE is a supported Fabric adapter, not the OPL Cloud product boundary.
This product repository owns reusable adapter source and generic render tools;
it does not own a TKE cluster, production environment, Secrets, deployment
dispatch, rollback, or runtime receipt.

The medopl.cn TKE profile and production workflow are owned only by the private
`gaofeng21cn/opl-instance-medopl` repository. That workflow consumes an
immutable OPL Cloud release identified by product SHA and image digest.

For a generic Docker installation, use [Install OPL Cloud](../installation.md).
