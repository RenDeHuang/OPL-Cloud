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
- Console auth users with positive integer `sub2apiUserId` values;
- `OPL_SUB2API_BASE_URL`, supported version list, timeout, and admin credentials;
- TKE kubeconfig, namespace, domains, TLS, storage class, and image-pull secret;
- OPL Cloud and Workspace image references;
- Tencent mutation credentials, region, cluster, subnet, and security groups;
- internal service token and AionUI password seed.

Secrets must be GitHub/Kubernetes secrets. Never place credentials in the
manifest, command arguments, logs, or verifier artifacts.

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

## PostgreSQL Capacity And Recovery

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

## Operational Alerts

`GET /api/operator/summary` derives `notifications` from current compute and
storage rows. It does not persist a second alert state:

| Code | Severity | Source state |
| --- | --- | --- |
| `manual_review` | error | `billingStatus=manual_review` |
| `past_due` | warning | `billingStatus=past_due` |
| `ledger_receipt_pending` | warning | `lastBillingError=ledger_receipt_pending` |
| `cleanup_failed` | error | cleanup/destroy failure code in `lastBillingError` |

Control Plane logs active and recovered transitions in this CLS-safe shape:

```text
event=opl_operational_state code=<stable-code> state=<active|recovered> resource_type=<compute|storage> resource_ref=<12-hex-hash>
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

## Deploy

Use the `Deploy TKE Production` workflow with immutable image references. It
installs secrets, renders the manifest, applies it, restarts all ConfigMap
consumers, and waits for each rollout.

Manual bounded rollout checks:

```bash
kubectl -n opl-cloud rollout status deployment/opl-cloud-control-plane --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-fabric --timeout=5m
kubectl -n opl-cloud rollout status deployment/opl-cloud-ledger --timeout=5m
```

Then check health and readiness through the production endpoints. An old image
digest or timeout is a failed rollout.

## Paid E2E

Use a dedicated mapped account with enough Sub2API balance. The exact guard is
mandatory:

```bash
OPL_CONSOLE_ORIGIN=https://cloud.medopl.cn \
OPL_VERIFY_AUTH_USERS_JSON='<secret auth seed>' \
OPL_VERIFY_PAID_CONFIRMATION=I_UNDERSTAND_THIS_SPENDS_REAL_BALANCE \
OPL_VERIFY_RUN_ID=<unique-run-id> \
npm run verify:production -- --browser-e2e
```

The verifier must prove:

1. one mapped account and live starting balance;
2. Basic compute charge of `50000000` USD micros;
3. 10 GB storage charge of `2571429` USD micros;
4. exact total balance delta of `52571429` USD micros;
5. stable redeem code per resource operation;
6. active monthly entitlements and two Ledger receipts;
7. compute, storage, attachment, and public Workspace URL readiness;
8. exact cleanup of only the run's resources.

Keep the run failed until cleanup is confirmed. Do not substitute broad cloud
cleanup commands for resource IDs recorded by the verifier.

## Billing Recovery

- `preparing`: inspect the Fabric operation; no charge is expected yet.
- `charge_pending`: replay the same persisted redeem code; never create a new code.
- `manual_review`: compare the exact Sub2API adjustment and balance snapshots
  before changing entitlement state.
- `active` with missing receipt: retry only the Ledger receipt write.
- expired compute: confirm provider deletion.
- expired storage: preserve data and block attachment until reactivation.

## Sub2API Updates

Sub2API stays on official images. Run the config repository's guarded updater:

```bash
cd /home/ubuntu/sub2api
bash tests/safe-update.test.sh
bash scripts/safe-update.sh
```

The updater accepts only an approved version/revision, probes login, version,
balance-read, and adjustment-route availability without changing a real balance,
and restores the previous digest on failure.
