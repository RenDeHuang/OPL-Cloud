# Tencent CVM Workspace Runtime

This folder is the production handoff for OPL Cloud route A:

```text
1 OPL Workspace
= 1 Tencent CVM server
= 1 CBS cloud disk
= 1 one-person-lab-app Docker container
= 1 stable URL
```

The Console API remains the control plane. OpenTofu creates the server and disk, Ansible configures Docker/Caddy, and the API stores the returned server, disk, Docker, and URL binding.

## Required Inputs

Set these as environment variables or OpenTofu variables:

- `TENCENTCLOUD_SECRET_ID`
- `TENCENTCLOUD_SECRET_KEY`
- `TENCENTCLOUD_REGION`
- `OPL_WORKSPACE_ID`
- `OPL_WORKSPACE_SLUG`
- `OPL_WORKSPACE_TOKEN`
- `OPL_WORKSPACE_PACKAGE`
- `OPL_WORKSPACE_IMAGE`
- `OPL_WORKSPACE_DOMAIN`
- `OPL_SSH_PUBLIC_KEY`
- `OPL_VPC_ID`
- `OPL_SUBNET_ID`
- `OPL_SECURITY_GROUP_ID`

## Commands

Plan only:

```bash
cd infra/tencent-cvm
cat > "$OPL_WORKSPACE_STATE_DIR/workspace.auto.tfvars.json" <<'JSON'
{
  "workspace_id": "ws-example",
  "workspace_slug": "example-lab",
  "workspace_token": "replace-with-workspace-token",
  "workspace_domain": "workspaces.oplcloud.cn",
  "owner_account_id": "pi-example",
  "package_id": "basic",
  "opl_image": "harbor.oplcloud.cn/opl/one-person-lab-webui:2026-07-01",
  "region": "ap-guangzhou",
  "availability_zone": "ap-guangzhou-6",
  "image_id": "img-...",
  "vpc_id": "vpc-...",
  "subnet_id": "subnet-...",
  "security_group_id": "sg-...",
  "key_id": "skey-..."
}
JSON
tofu init
tofu plan \
  -var-file="$OPL_WORKSPACE_STATE_DIR/workspace.auto.tfvars.json"
```

Apply:

```bash
tofu apply \
  -var-file="$OPL_WORKSPACE_STATE_DIR/workspace.auto.tfvars.json"
```

Configure the server:

```bash
cat > "$OPL_WORKSPACE_STATE_DIR/ansible-vars.json" <<'JSON'
{
  "workspace_id": "ws-example",
  "workspace_slug": "example-lab",
  "workspace_token": "replace-with-workspace-token",
  "workspace_domain": "workspaces.oplcloud.cn",
  "opl_image": "harbor.oplcloud.cn/opl/one-person-lab-webui:2026-07-01"
}
JSON
ansible-playbook -i "<public_ip>," ansible/workspace.yml \
  -u root \
  --extra-vars "@$OPL_WORKSPACE_STATE_DIR/ansible-vars.json"
```

The API provider writes these per-Workspace variable files under ignored `.runtime/tencent-cvm/<workspaceId>/`. Do not pass `workspace_token` directly on the `tofu` or `ansible-playbook` command line.

## Lifecycle Boundary

- `stopServer`: runs `tccli cvm StopInstances --StoppedMode STOP_CHARGING`. Keep CBS disk.
- `restartServer`: runs `tccli cvm StartInstances` for stopped servers. Preserve URL/token.
- `recreateServer`: after server destruction, runs `tccli cvm RunInstances`, `tccli cbs AttachDisks`, `tccli cvm DescribeInstances`, then Ansible to restore Docker/Caddy on the new CVM with the retained CBS disk. Preserve URL/token.
- `destroyServer`: stops CVM if needed, runs `tccli cbs DetachDisks`, then `tccli cvm TerminateInstances`. Keep CBS disk.
- `destroyDisk`: runs `tccli cbs TerminateDisks` after explicit data-loss confirmation. This is the only operation that stops storage billing.

The API host must have `tccli` configured with Tencent credentials from the environment or a deployment secret manager.

## Production Notes

- Harbor is the production source for `OPL_WORKSPACE_IMAGE`.
- The attached CBS data disk is formatted as ext4 when needed and mounted at `/data/opl` before Docker starts. The `one-person-lab-app` container maps `/data/opl/data` to `/data` and `/data/opl/projects` to `/projects`.
- The Docker/WebUI contract is port `3000`, persistent `/data`, persistent `/projects`, and no API/model keys passed through CLI, env, or Compose.
- Caddy owns HTTPS and token URL routing. The Ansible playbook installs Caddy, writes `/etc/caddy/Caddyfile`, imports `/etc/caddy/conf.d/*.caddy`, and fails the run if Caddy cannot reload the Workspace route.
- OpenMeter receives `server_debit` and `storage_debit` usage events from the API billing settlement path.
- Lago is not required for the current closed loop; use it later for invoices/subscriptions.
