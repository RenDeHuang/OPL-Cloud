# Production Runbook

## Readiness

```text
GET /api/runtime/readiness
GET /api/production/readiness
```

Validate deployment secret references:

```bash
npm run validate:production-manifest -- \
  --manifest deploy/production-manifest.example.json
```

## Required Inputs

- PostgreSQL `DATABASE_URL`;
- Sub2API admin credentials whose live identity is `admin@medopl.cn`;
- server-only `OPL_SUB2API_BASE_URL`, request timeout, and the required capability inventory;
- TKE kubeconfig, namespace, domains, TLS, storage class, and image-pull secret;
- OPL Cloud and Workspace image references;
- Tencent mutation credentials, region, cluster, subnet, and security groups;
- internal service token and AionUI password seed.

Secrets must be GitHub/Kubernetes secrets. New customer Workspace Gateway Keys
use Workspace-scoped Kubernetes Secrets written by Fabric, not a global
deployment environment variable. Existing account-scoped Secrets are read-only
compatibility until the target Workspace's first Key rotation. Never place
credentials in the manifest, command arguments, logs, or verifier artifacts.

Console exposes no Gateway base-address API or card. Never expose
`OPL_SUB2API_BASE_URL` or link ordinary users to the Sub2API backend. Cloud does
not inject `OPL_CODEX_BASE_URL` into Runtime.

`OPL_CONSOLE_USERS_JSON` is retired and any non-empty value makes Control Plane
startup fail closed. The deploy workflow and manifest no longer install that
seed. Provision customers through `POST /api/operator/accounts` with
`ProvisionAccountRequest`; the backend resolves or creates one Sub2API identity
by normalized email and atomically stores the local mapping.

Workspace file bodies remain only on CBS. The PostgreSQL procedures below
protect platform identity, operation, resource-reference, and receipt state;
they never copy, back up, restore, sync, or transfer Workspace files.

## Database Startup Migrations

Control Plane, Fabric, and Ledger share a database-wide advisory lock during
startup migration and record successful versions in `opl_schema_migrations`.
Later starts perform version reads only; they do not replay completed DDL or
backfills. A failed version is not recorded as successful.

After a release that adds a migration, inspect the journal with read-only SQL:

```sql
SELECT service, version, applied_at
FROM opl_schema_migrations
ORDER BY service, version;
```

For a no-migration restart, capture the result before and after rollout. The row
set and `applied_at` values must remain unchanged. Correlate the same window with
PostgreSQL CPU, WAL generation, and storage metrics; a repeated DDL, bulk UPDATE,
or migration-related WAL spike is a failed rollout.

## Platform PostgreSQL Capacity And Recovery

Run these read-only statements as the database administrator before sizing or
recovery work:

```sql
SELECT pg_size_pretty(COALESCE(sum(size), 0)) AS wal_size
FROM pg_ls_waldir();

SHOW max_wal_size;
SHOW min_wal_size;
SHOW wal_keep_size;

SELECT slot_name, slot_type, active, restart_lsn,
       pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) AS retained_wal
FROM pg_replication_slots
ORDER BY slot_name;

SELECT CASE current_setting('logging_collector')
         WHEN 'on' THEN (
           SELECT pg_size_pretty(COALESCE(sum(size), 0))
           FROM pg_ls_logdir()
         )
         ELSE 'logging_collector=off'
       END AS text_log_size;

SELECT datname, pg_size_pretty(pg_database_size(datname)) AS database_size
FROM pg_database
WHERE datallowconn
ORDER BY pg_database_size(datname) DESC;
```

Treat the current approximately 8.1 GB `LogFileSize` observation as WAL until
the SQL and TencentDB metrics distinguish it from text logs. Never delete
`pg_wal` or files below it. Check inactive replication slots and WAL settings,
then use supported TencentDB controls to correct retention.

Use fixed storage for the final instance. Size it so the restored database is
below 60% `StorageRate` while idle, including WAL and operating headroom. Keep
automatic storage expansion disabled unless measured growth later justifies it;
the 75% and 85% alarms below are the expansion decision points.

In **TencentDB for PostgreSQL > Instance List**, open the source instance and:

1. Under **Backup and Restore > Backup Settings**, enable automatic backups,
   select the required backup days and a low-traffic backup window, set the
   retention period, and save. Record those settings outside the repository.
2. In **Backup List**, wait for an automatic backup whose status is successful.
   A manual backup does not prove that the automatic schedule works.
3. Select that backup and choose its restore-to-new-instance action. Creating
   the isolated instance is billable and requires explicit approval immediately
   before execution. Use the same region and private VPC, no public endpoint, a
   restricted security group, and fixed storage sized by the rule above.
4. Do not point production at the restored instance. First connect with a
   read-only account and run the checks below. Keep the source instance intact.

If the source contains no account mappings, resource facts, or receipts worth
retaining, initialize the final instance cleanly instead of copying empty history.
Do not delete the old instance until backup settings, isolated restore, SQL
checks, application cutover, and a rollback window have all been verified.

Validate restored application truth without printing row data:

```sql
SELECT count(*) AS accounts,
       count(*) FILTER (WHERE sub2api_user_id > 0) AS mapped_accounts,
       count(*) FILTER (WHERE sub2api_user_id <= 0) AS invalid_mappings
FROM control_plane_accounts;

SELECT status, billing_status, count(*)
FROM control_plane_compute_allocations
GROUP BY status, billing_status
ORDER BY status, billing_status;

SELECT status, billing_status, count(*)
FROM control_plane_storage_volumes
GROUP BY status, billing_status
ORDER BY status, billing_status;

SELECT resource_kind, status, count(*)
FROM fabric_operations
GROUP BY resource_kind, status
ORDER BY resource_kind, status;

SELECT status, count(*)
FROM machine_ownerships
GROUP BY status
ORDER BY status;

SELECT receipt_type, status, count(*)
FROM evidence_receipts
GROUP BY receipt_type, status
ORDER BY receipt_type, status;

SELECT service, version, applied_at
FROM opl_schema_migrations
ORDER BY service, version;
```

After an approved cutover, roll out all three services with the same immutable
image, then verify that each Deployment is available and each service answers:

```bash
kubectl -n opl-cloud rollout status deployment/opl-cloud-control-plane --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-fabric --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-ledger --timeout=5m

curl --fail --silent --show-error https://cloud.medopl.cn/api/healthz
curl --fail --silent --show-error https://cloud.medopl.cn/api/runtime/readiness
curl --fail --silent --show-error https://cloud.medopl.cn/api/production/readiness
```

Use temporary local port-forwards to check the internal services. Run each
port-forward in one terminal and its matching `curl` in another, then stop the
port-forward; do not expose either service publicly:

```bash
kubectl -n opl-cloud port-forward service/opl-cloud-fabric 18082:8082
curl --fail --silent --show-error http://127.0.0.1:18082/healthz

kubectl -n opl-cloud port-forward service/opl-cloud-ledger 18081:8081
curl --fail --silent --show-error http://127.0.0.1:18081/healthz
```

Re-run the count queries after cutover and compare them with the isolated-restore
results. Also compare the migration journal and PostgreSQL CPU/WAL graphs across
one restart. No migration rows, DDL/backfill activity, or restart-related WAL/CPU
peak may appear.

These TencentDB backup, restore, sizing, and Cloud Monitor steps are operator
actions. Repository tests cannot establish that they happened; retain console
screenshots or exported metric evidence in the operations system, not in Git.

## Single-Pod Capacity Gate

The final local code-complete gate is machine checked. Node TAP output must
contain exactly one zero-skipped summary. Go suites run with `-json`, and any
event with `Action=skip` fails the command. Every PostgreSQL suite sets
`OPL_POSTGRES_TESTS=1`; a claimed zero-SKIP Control Plane full run also sets
`OPL_CAPACITY_TESTS=1`. If the capacity suite cannot run, cancel the global
zero-SKIP/code-complete claim rather than downgrading it manually. The exact
stdlib-only parsers and commands are maintained in
`.github/workflows/pull-request-ci.yml`.

Run the opt-in load test against a local or isolated PostgreSQL instance. It uses
fake Sub2API, Fabric, and Ledger clients and never creates cloud resources or a
real charge:

```bash
cd services/control-plane
OPL_CAPACITY_TESTS=1 go test ./internal/server \
  -run '^TestSinglePodCapacity$' -count=1 -v -timeout=15m
```

The historical gate creates an isolated schema, seeds 1,000 accounts and
resource rows, sends 100 concurrent Console requests, and replays 20 resource
commands. Its 1,000-resource renewal scan predates the Workspace-level renewal
saga and is not current renewal evidence. The current isolated PostgreSQL
Workspace renewal suites must run without SKIP; do not use the historical scan
to claim Pilot capacity or renewal readiness.

The accepted local baseline used Go 1.22.2, PostgreSQL 16.14, and an i7-8700 host:

| Workload | P50 | P95 | Error rate | Completion |
| --- | ---: | ---: | ---: | ---: |
| 100 concurrent Console requests | 1.903 s | 2.064 s | 0% | 2.098 s |
| 20 concurrent resource commands | 643 ms | 710 ms | 0% | 710 ms |
| 20 same-key replays | 401 ms | 402 ms | 0% | 402 ms |
| 1,000 renewal scan | n/a | n/a | 0% | 97.38 s |

The measured process used 96.99 seconds of CPU, grew from 1.0 MiB to 13.6 MiB
heap, reported 158.8 MiB Go system memory, and opened at most 20 application
database connections. The host could not enforce the production `500m` CPU
limit, so these are local capacity facts rather than a production-quota claim.
Keep one Control Plane Pod while this gate and production alarms pass. A breach
requires profiling and query correction first; it is not automatic approval for
additional replicas.

## Operational Alerts

`GET /api/operator/summary` derives `notifications` from Workspace renewal
operations plus compute/storage compatibility facts. It does not persist a
second alert state:

| Code | Severity | Source state |
| --- | --- | --- |
| `manual_review` | error | renewal or launch requires operator decision |
| `cleanup_failed`, `cleanup_pending` | error | provider cleanup did not converge |
| `past_due`, `insufficient` | warning | Workspace period cannot be extended |
| `ledger_receipt_pending`, `renewal_receipt_pending`, `refund_receipt_pending`, `expiry_receipt_pending` | warning | external side effect is stable; only evidence remains |
| `renewal_retry_pending` | warning | same Workspace renewal operation must resume |

Control Plane logs active and recovered transitions in this CLS-safe shape:

```text
event=opl_operational_state code=<stable-code> state=<active|recovered> resource_type=<workspace|compute|storage> resource_ref=<12-hex-hash>
```

The line must never contain an account ID, raw resource ID, redeem code, balance,
credential, provider payload, or provider error. Configure one CLS alarm per error
code and one grouped warning alarm; match `state=active`, and use `state=recovered`
as the recovery event.

In Tencent Cloud Console, create an `OPL Cloud Operations` notification template
under **Cloud Monitor > Alarm Management > Notification Templates**. Add enterprise
WeChat and email recipients, add SMS for P1 policies, and enable recovery notices.
Then create these policies under **Alarm Policies**:

| Resource | Condition | Level |
| --- | --- | --- |
| TencentDB for PostgreSQL production instance | `StorageRate >= 75%` for 10 minutes | warning |
| TencentDB for PostgreSQL production instance | `StorageRate >= 85%` for 5 minutes | P1 |
| TencentDB for PostgreSQL production instance | CPU utilization `>= 70%` for 10 minutes | warning |
| TencentDB for PostgreSQL production instance | CPU utilization `>= 85%` for 5 minutes | P1 |
| TKE namespace `opl-cloud` | any OPL Cloud Pod restart increase within 5 minutes | warning |
| TKE Deployments `opl-cloud-control-plane`, `opl-cloud-fabric`, `opl-cloud-ledger` | unavailable replicas `>= 1` for 5 minutes | P1 |
| CLB attached to Ingress `opl-cloud` | HTTP 5xx count `> 0` for 5 minutes | P1 |

Create a Cloud Monitor URL test for
`https://cloud.medopl.cn/api/healthz` at one-minute intervals. Trigger P1 after
three consecutive non-200 responses or timeouts and send a recovery notice after
the endpoint returns 200. Bind every policy to `OPL Cloud Operations`.

Before saving policies, use the metric selector in the current console (or
read-only `DescribeBaseMetrics`) to confirm the metric belongs to the selected
PostgreSQL/TKE/CLB resource. Do not copy an unverified namespace or instance ID
from documentation, and do not store notification credentials in this repository.

## Workspace Routing Verification

Repository configuration declares one shared `qcloud` Ingress with `/` paths
for `cloud.medopl.cn` and `workspace.medopl.cn`, both targeting Control Plane.
Fabric creates a Deployment, ClusterIP Service, and Secret per Workspace; it
does not create a per-Workspace Ingress. The deploy workflow renders the shared
Ingress only for bootstrap and normally applies with `--skip-shared-ingress`, so
the repository manifest is not evidence of the current live listener rules.

Public read-only checks currently prove that `workspace.medopl.cn` resolves to
the shared CLB, presents a valid certificate for that hostname, and reaches
Control Plane. They do not prove the exact live rule count, HTTP/2 value,
WebSocket forwarding, CLB access controls, backend health, or account quota.
Run the following from the VPC-capable operator environment; the local default
Tencent credential is not valid and must not be copied into this repository:

```bash
kubectl --kubeconfig "$KUBECONFIG" -n opl-cloud get ingress opl-cloud -o json
kubectl --kubeconfig "$KUBECONFIG" -n opl-cloud get tkeserviceconfigs.cloud.tencent.com opl-cloud-ingress-config -o json

kubectl --kubeconfig "$KUBECONFIG" -n opl-cloud get ingress opl-cloud -o json \
  | jq '{pathCount: ([.spec.rules[].http.paths[]] | length), rules: [.spec.rules[] | {host, paths: [.http.paths[] | {path, pathType, backend: .backend.service}]}]}'

openssl s_client -connect workspace.medopl.cn:443 -servername workspace.medopl.cn \
  -verify_return_error </dev/null 2>/dev/null \
  | openssl x509 -noout -subject -issuer -dates -ext subjectAltName
```

Resolve the current public VIP, then query the matching CLB. Replace only the
placeholders below; never put Tencent credentials in command arguments or logs:

```bash
dig +short workspace.medopl.cn

tccli --region na-siliconvalley clb DescribeLoadBalancers \
  --LoadBalancerVips '["<workspace-public-vip>"]' --Limit 20

tccli --region na-siliconvalley clb DescribeListeners \
  --LoadBalancerId <load-balancer-id> --Protocol HTTPS --Port 443

tccli --region na-siliconvalley clb DescribeTargets \
  --LoadBalancerId <load-balancer-id> --Protocol HTTPS --Port 443

tccli --region na-siliconvalley clb DescribeTargetHealth \
  --LoadBalancerIds '["<load-balancer-id>"]'

tccli --region na-siliconvalley clb DescribeLBOperateProtect \
  --LoadBalancerIds '["<load-balancer-id>"]'

tccli --region na-siliconvalley clb DescribeQuota
```

Record only redacted evidence. For the HTTPS listener, verify the certificate
ID, `SniSwitch`, and every rule's `Domain`, `Url`, `Http2`, `ForwardType`,
`OAuth`, `WafDomainId`, and location ID. Use `DescribeTargetHealth` rather than
the target-binding response to verify `HealthStatus` and `HealthStatusDetail`.
On the CLB record, verify `SecureGroups`; use `DescribeLBOperateProtect` for
deletion protection. Count live `Rules` entries and compare that number with
`TOTAL_LISTENER_RULE_QUOTA.QuotaLimit`; Tencent may return a null
`QuotaCurrent`, so it is not a substitute for counting rules.

WebSocket support is complete only when an already authorized Workspace browser
gets HTTP 101 on `/ws` and exchanges frames through this exact CLB rule. A 404,
ordinary 2xx page, repository annotation, or vendor capability statement is not
evidence. Do not create a paid Workspace solely for this check, and never paste
an authentication cookie into a shell command or artifact.

Before any later route cleanup, compare live listener location IDs with the live
Ingress, TkeServiceConfig, Fabric runtime Services, and active Workspace facts.
Any unmatched rule is an orphan candidate, not deletion authorization. Route
mutation and any dedicated Workspace Router or changed access-security model
require separate approval.

## Deploy

Use the `Deploy TKE Production` workflow with immutable image references. It
installs secrets, snapshots the current ConfigMap and Cloud images, renders the
manifest, applies it, and waits for the three Cloud rollouts. It updates the
immutable Workspace image default used by new Fabric operations, but does not
restart or wait for existing Workspace Deployments while Runtime/Workspace rollout is
paused. The current internal PostgreSQL endpoint has no TLS, so the manifest
sets `PGSSLMODE=disable`. The application accepts that exception only for one
RFC1918 IPv4 literal in `DATABASE_URL`; `verify-full` remains required for every
other endpoint.

Set `diagnostics_only=true` to read Nodes, Cloud/Workspace Pod state, Events,
and Cloud container logs without applying a manifest or changing a workload.
On a failed deploy, rollback restores the complete previous ConfigMap data
before restoring the three previous Cloud images.

Manual bounded rollout checks:

```bash
kubectl -n opl-cloud rollout status deployment/opl-cloud-control-plane --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-fabric --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-ledger --timeout=5m
```

Then check health and readiness through the production endpoints. An old image
digest or timeout is a failed rollout.

## Paused Provider Verification

Provider Acceptance, Pro verification, and fixed-slot verification are
paused and do not gate ordinary Basic rollout. Do not run the legacy paid
verifier.

The only commands authorized without a new owner approval are read-only:

```bash
node tools/production-verifier.ts --read-only
node tools/production-live-qa.ts --read-only
node tools/provider-acceptance.ts --read-only
```

Ordinary CI, release, E2E, deployment manifests, and deployed runtime
environments do not carry Acceptance accounts, `OPL_VERIFY_*`, Provider
Acceptance credentials, or a protected Acceptance Environment dependency.

Provider Acceptance separately creates or adopts these retained non-customer
slots after read-only inventory and explicit approval:

- `verification-slot-basic-01`: `SA5.MEDIUM4`, 2c4g, 10GB CBS;
- `verification-slot-pro-01`: `SA5.2XLARGE16`, 8c16g, 100GB CBS.

Ordinary deploy performs only rollout and readiness checks. The normal Console
Basic canary runs separately once after rollout; repeated release smoke checks
must stay read-only and never buy a second Workspace package.

## Billing Recovery

- `preflight`: read-only checks only; no debit or provider write is allowed.
- `debit_pending`: replay the same persisted Redeem Code and Idempotency-Key;
  confirm a lost response through exact Sub2API balance-history evidence.
- debit succeeded with confirmed no billable resource: replay one deterministic
  refund identity and verify its readback.
- debit succeeded with partial or unknown provider result: enter `manual_review`
  without refund, cleanup, or repurchase.
- each Workspace renewal owns one combined debit and the same CVM/CBS IDs. Resume
  from its persisted phase; never run independent customer compute/storage renewal.
- active entitlement with a missing receipt retries only the Ledger write.
- unpaid expiry denies access and records
  `providerAction=none_expire_by_provider`; it performs zero Fabric/Tencent stop,
  renew, destroy, or delete mutations.
- `autoRenew=true` is unavailable to Pilot users until a real approved renewal
  has been proven.

## Sub2API Updates

This repository does not deploy or update Sub2API. Any Sub2API change is a
separate external operations change with its own approval, rollback, and
capability verification. OPL Cloud release procedures only perform the approved
read-only capability inventory against the deployed service.
