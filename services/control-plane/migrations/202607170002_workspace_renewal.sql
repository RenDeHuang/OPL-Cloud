ALTER TABLE control_plane_workspaces
  ADD COLUMN IF NOT EXISTS billing_state_json TEXT NOT NULL DEFAULT '{}';

CREATE OR REPLACE FUNCTION pg_temp.opl_try_jsonb(input_text TEXT)
RETURNS JSONB
LANGUAGE plpgsql
AS $$
BEGIN
  RETURN input_text::jsonb;
EXCEPTION WHEN OTHERS THEN
  RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION pg_temp.opl_try_timestamptz(input_text TEXT)
RETURNS TIMESTAMPTZ
LANGUAGE plpgsql
AS $$
BEGIN
  RETURN input_text::timestamptz;
EXCEPTION WHEN OTHERS THEN
  RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION pg_temp.opl_is_rfc3339(input_text TEXT)
RETURNS BOOLEAN
LANGUAGE sql
IMMUTABLE
AS $$
  SELECT COALESCE(
    input_text ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$',
    FALSE
  );
$$;

CREATE OR REPLACE FUNCTION pg_temp.opl_rfc3339(input_time TIMESTAMPTZ)
RETURNS TEXT
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
  SELECT
    to_char(input_time AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS')
    || CASE
      WHEN to_char(input_time AT TIME ZONE 'UTC', 'US') = '000000' THEN ''
      ELSE '.' || rtrim(to_char(input_time AT TIME ZONE 'UTC', 'US'), '0')
    END
    || 'Z';
$$;

WITH legacy AS (
  SELECT
    w.id AS workspace_id,
    COALESCE(NULLIF(w.account_id, ''), w.owner_account_id) AS workspace_account_id,
    w.account_id,
    w.owner_account_id,
    w.owner_user_id AS workspace_owner_user_id,
    u.id AS owner_user_record_id,
    u.account_id AS owner_user_account_id,
    u.status AS owner_user_status,
    u.role AS owner_user_role,
    w.current_compute_allocation_id,
    w.storage_id,
    c.id AS compute_id,
    c.account_id AS compute_account_id,
    c.owner_user_id AS compute_owner_user_id,
    c.workspace_id AS compute_workspace_id,
    c.package_id AS compute_package_id,
    c.billing_status AS compute_billing_status,
    pg_temp.opl_try_jsonb(c.billing_state_json) AS compute_billing,
    s.id AS storage_record_id,
    s.account_id AS storage_account_id,
    s.owner_user_id AS storage_owner_user_id,
    s.workspace_id AS storage_workspace_id,
    s.package_id AS storage_package_id,
    s.billing_status AS storage_billing_status,
    s.size_gb AS storage_gb,
    pg_temp.opl_try_jsonb(s.billing_state_json) AS storage_billing
  FROM control_plane_workspaces w
  LEFT JOIN control_plane_users u ON u.id = w.owner_user_id
  LEFT JOIN control_plane_compute_allocations c ON c.id = w.current_compute_allocation_id
  LEFT JOIN control_plane_storage_volumes s ON s.id = w.storage_id
  WHERE w.billing_state_json IS NULL
     OR btrim(w.billing_state_json) = ''
     OR pg_temp.opl_try_jsonb(w.billing_state_json) = '{}'::jsonb
), facts AS (
  SELECT
    legacy.*,
    pg_temp.opl_try_timestamptz(compute_billing ->> 'periodStart') AS compute_period_start,
    pg_temp.opl_try_timestamptz(storage_billing ->> 'periodStart') AS storage_period_start,
    pg_temp.opl_try_timestamptz(compute_billing ->> 'paidThrough') AS compute_paid_through,
    pg_temp.opl_try_timestamptz(storage_billing ->> 'paidThrough') AS storage_paid_through,
    COALESCE(NULLIF(compute_billing ->> 'deadline', ''), compute_billing #>> '{providerData,deadline}') AS compute_deadline_text,
    COALESCE(NULLIF(storage_billing ->> 'deadline', ''), storage_billing #>> '{providerData,deadline}') AS storage_deadline_text,
    pg_temp.opl_try_timestamptz(COALESCE(NULLIF(compute_billing ->> 'deadline', ''), compute_billing #>> '{providerData,deadline}')) AS compute_deadline,
    pg_temp.opl_try_timestamptz(COALESCE(NULLIF(storage_billing ->> 'deadline', ''), storage_billing #>> '{providerData,deadline}')) AS storage_deadline
  FROM legacy
), backfill AS (
  SELECT
    workspace_id,
    CASE WHEN
      workspace_account_id <> ''
      AND (account_id = '' OR owner_account_id = '' OR account_id = owner_account_id)
      AND workspace_owner_user_id <> ''
      AND owner_user_record_id = workspace_owner_user_id
      AND owner_user_account_id = workspace_account_id
      AND owner_user_status = 'active'
      AND owner_user_role = 'owner'
      AND compute_id = current_compute_allocation_id
      AND storage_record_id = storage_id
      AND compute_account_id = workspace_account_id
      AND storage_account_id = workspace_account_id
      AND compute_owner_user_id = workspace_owner_user_id
      AND storage_owner_user_id = workspace_owner_user_id
      AND compute_workspace_id = workspace_id
      AND storage_workspace_id = workspace_id
      AND compute_billing_status = 'active'
      AND storage_billing_status = 'active'
      AND compute_package_id = storage_package_id
      AND compute_package_id IN ('basic', 'pro')
      AND ((compute_package_id = 'basic' AND storage_gb = 10) OR (compute_package_id = 'pro' AND storage_gb = 100))
      AND jsonb_typeof(compute_billing -> 'autoRenew') = 'boolean'
      AND jsonb_typeof(storage_billing -> 'autoRenew') = 'boolean'
      AND compute_billing ->> 'autoRenew' = 'false'
      AND storage_billing ->> 'autoRenew' = 'false'
      AND compute_billing ->> 'priceVersion' = 'pilot-usd-2026-07-v1'
      AND storage_billing ->> 'priceVersion' = 'pilot-usd-2026-07-v1'
      AND compute_billing ->> 'currency' = 'USD'
      AND storage_billing ->> 'currency' = 'USD'
      AND compute_billing #>> '{priceSnapshot,resourceType}' = 'compute'
      AND storage_billing #>> '{priceSnapshot,resourceType}' = 'storage'
      AND compute_billing #>> '{priceSnapshot,priceVersion}' = 'pilot-usd-2026-07-v1'
      AND storage_billing #>> '{priceSnapshot,priceVersion}' = 'pilot-usd-2026-07-v1'
      AND compute_billing #>> '{priceSnapshot,packageId}' = compute_package_id
      AND storage_billing #>> '{priceSnapshot,packageId}' = storage_package_id
      AND compute_billing #>> '{priceSnapshot,currency}' = 'USD'
      AND storage_billing #>> '{priceSnapshot,currency}' = 'USD'
      AND compute_billing #>> '{priceSnapshot,billingUnit}' = 'calendar_month'
      AND storage_billing #>> '{priceSnapshot,billingUnit}' = 'calendar_month'
      AND storage_billing #>> '{priceSnapshot,sizeGb}' = CASE compute_package_id WHEN 'basic' THEN '10' ELSE '100' END
      AND storage_billing ->> 'computeAllocationId' = compute_id
      AND jsonb_typeof(compute_billing -> 'chargeUsdMicros') = 'number'
      AND jsonb_typeof(storage_billing -> 'chargeUsdMicros') = 'number'
      AND compute_billing ->> 'chargeUsdMicros' = CASE compute_package_id WHEN 'basic' THEN '50000000' ELSE '214280000' END
      AND storage_billing ->> 'chargeUsdMicros' = CASE storage_package_id WHEN 'basic' THEN '2580000' ELSE '25800000' END
      AND compute_billing #>> '{priceSnapshot,chargeUsdMicros}' = compute_billing ->> 'chargeUsdMicros'
      AND storage_billing #>> '{priceSnapshot,chargeUsdMicros}' = storage_billing ->> 'chargeUsdMicros'
      AND jsonb_typeof(storage_billing -> 'billingAnchorDay') = 'number'
      AND storage_billing ->> 'billingAnchorDay' = compute_billing ->> 'billingAnchorDay'
      AND CASE
        WHEN jsonb_typeof(compute_billing -> 'billingAnchorDay') = 'number'
          AND compute_billing ->> 'billingAnchorDay' ~ '^[1-9][0-9]?$'
        THEN (compute_billing ->> 'billingAnchorDay')::bigint BETWEEN 1 AND 31
        ELSE FALSE
      END
      AND pg_temp.opl_is_rfc3339(compute_billing ->> 'periodStart')
      AND pg_temp.opl_is_rfc3339(storage_billing ->> 'periodStart')
      AND pg_temp.opl_is_rfc3339(compute_billing ->> 'paidThrough')
      AND pg_temp.opl_is_rfc3339(storage_billing ->> 'paidThrough')
      AND compute_period_start IS NOT NULL
      AND storage_period_start = compute_period_start
      AND compute_paid_through IS NOT NULL
      AND storage_paid_through = compute_paid_through
      AND isfinite(compute_period_start)
      AND isfinite(compute_paid_through)
      AND compute_paid_through > compute_period_start
      AND pg_temp.opl_is_rfc3339(compute_deadline_text)
      AND pg_temp.opl_is_rfc3339(storage_deadline_text)
      AND compute_deadline IS NOT NULL
      AND storage_deadline IS NOT NULL
      AND isfinite(compute_deadline)
      AND isfinite(storage_deadline)
      AND compute_deadline >= compute_paid_through
      AND storage_deadline >= compute_paid_through
      AND (
        compute_billing #>> '{providerData,deadline}' IS NULL
        OR compute_billing ->> 'deadline' IS NULL
        OR pg_temp.opl_try_timestamptz(compute_billing #>> '{providerData,deadline}') = compute_deadline
      )
      AND (
        storage_billing #>> '{providerData,deadline}' IS NULL
        OR storage_billing ->> 'deadline' IS NULL
        OR pg_temp.opl_try_timestamptz(storage_billing #>> '{providerData,deadline}') = storage_deadline
      )
    THEN jsonb_build_object(
      'autoRenew', false,
      'authorizedBy', '',
      'authorizedAt', '',
      'packageId', compute_package_id,
      'storageGb', storage_gb::bigint,
      'priceVersion', 'pilot-usd-2026-07-v1',
      'currency', 'USD',
      'billingUnit', 'calendar_month',
      'computeUsdMicros', (compute_billing ->> 'chargeUsdMicros')::bigint,
      'storageUsdMicros', (storage_billing ->> 'chargeUsdMicros')::bigint,
      'totalUsdMicros', (compute_billing ->> 'chargeUsdMicros')::bigint + (storage_billing ->> 'chargeUsdMicros')::bigint,
      'periodStart', pg_temp.opl_rfc3339(compute_period_start),
      'paidThrough', pg_temp.opl_rfc3339(compute_paid_through),
      'nextRenewalAt', pg_temp.opl_rfc3339(compute_paid_through - INTERVAL '24 hours'),
      'billingAnchorDay', (compute_billing ->> 'billingAnchorDay')::bigint,
      'renewalStatus', 'active',
      'computeAllocationId', compute_id,
      'storageId', storage_record_id
    )::text
    ELSE jsonb_build_object(
      'autoRenew', false,
      'renewalStatus', 'manual_review',
      'manualReviewReason', 'legacy_billing_state_mismatch'
    )::text END AS billing_state_json
  FROM facts
)
UPDATE control_plane_workspaces w
SET billing_state_json = backfill.billing_state_json
FROM backfill
WHERE w.id = backfill.workspace_id;

ALTER TABLE control_plane_workspaces
  ALTER COLUMN billing_state_json SET DEFAULT '{}',
  ALTER COLUMN billing_state_json SET NOT NULL;
