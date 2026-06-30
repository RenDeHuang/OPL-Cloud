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
tofu init
tofu plan \
  -var workspace_id="$OPL_WORKSPACE_ID" \
  -var workspace_slug="$OPL_WORKSPACE_SLUG" \
  -var workspace_token="$OPL_WORKSPACE_TOKEN"
```

Apply:

```bash
tofu apply \
  -var workspace_id="$OPL_WORKSPACE_ID" \
  -var workspace_slug="$OPL_WORKSPACE_SLUG" \
  -var workspace_token="$OPL_WORKSPACE_TOKEN"
```

Configure the server:

```bash
ansible-playbook -i "<public_ip>," ansible/workspace.yml \
  -u root \
  --extra-vars "workspace_id=$OPL_WORKSPACE_ID workspace_slug=$OPL_WORKSPACE_SLUG workspace_token=$OPL_WORKSPACE_TOKEN workspace_domain=$OPL_WORKSPACE_DOMAIN opl_image=$OPL_WORKSPACE_IMAGE"
```

## Lifecycle Boundary

- `stopServer`: stop CVM/server billing only. Keep CBS disk.
- `restartServer`: start or recreate CVM, reattach CBS disk, preserve URL/token.
- `destroyServer`: destroy CVM. Keep CBS disk.
- `destroyDisk`: destroy CBS disk after explicit data-loss confirmation. This is the only operation that stops storage billing.

## Production Notes

- Harbor is the production source for `OPL_WORKSPACE_IMAGE`.
- Caddy owns HTTPS and token URL routing.
- OpenMeter receives `server_debit` and `storage_debit` usage events from the API billing settlement path.
- Lago is not required for the current closed loop; use it later for invoices/subscriptions.
