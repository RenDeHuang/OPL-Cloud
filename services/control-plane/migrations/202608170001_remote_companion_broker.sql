CREATE TABLE IF NOT EXISTS control_plane_remote_seat_capacities (
  id TEXT PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  seat_count INTEGER NOT NULL CHECK (seat_count >= 0),
  seat_limit INTEGER NOT NULL DEFAULT 40 CHECK (seat_limit > 0),
  warning_threshold INTEGER NOT NULL DEFAULT 35 CHECK (warning_threshold > 0 AND warning_threshold <= seat_limit)
);

INSERT INTO control_plane_remote_seat_capacities
  (id, created_at, updated_at, seat_count, seat_limit, warning_threshold)
VALUES ('remote-companion', now(), now(), 0, 40, 35)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS control_plane_remote_invitations (
  id TEXT PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  secret_hash TEXT NOT NULL,
  secret_salt TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  consumed_at TEXT NOT NULL DEFAULT '',
  created_by_user_id TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS control_plane_remote_invitations_active_key
  ON control_plane_remote_invitations (status, expires_at, id);

CREATE TABLE IF NOT EXISTS control_plane_remote_pairings (
  id TEXT PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  invitation_id TEXT NOT NULL,
  claim_secret_hash TEXT NOT NULL,
  claim_secret_salt TEXT NOT NULL,
  manual_code_hash TEXT NOT NULL,
  manual_code_salt TEXT NOT NULL,
  manual_attempts INTEGER NOT NULL DEFAULT 0 CHECK (manual_attempts >= 0),
  state TEXT NOT NULL DEFAULT 'reserved',
  expires_at TEXT NOT NULL,
  reservation_expires_at TEXT NOT NULL,
  desktop_device_id TEXT NOT NULL DEFAULT '',
  desktop_device_label TEXT NOT NULL DEFAULT '',
  ios_device_id TEXT NOT NULL DEFAULT '',
  ios_device_label TEXT NOT NULL DEFAULT '',
  desktop_public_key TEXT NOT NULL,
  ios_public_key TEXT NOT NULL DEFAULT '',
  sas TEXT NOT NULL DEFAULT '',
  desktop_provider_user_id TEXT NOT NULL DEFAULT '',
  ios_provider_user_id TEXT NOT NULL DEFAULT '',
  desktop_provider_absent BOOLEAN NOT NULL DEFAULT FALSE,
  ios_provider_absent BOOLEAN NOT NULL DEFAULT FALSE,
  confirmed_at TEXT NOT NULL DEFAULT '',
  revoked_at TEXT NOT NULL DEFAULT '',
  seat_released BOOLEAN NOT NULL DEFAULT FALSE,
  create_idempotency_key TEXT NOT NULL DEFAULT '',
  create_request_hash TEXT NOT NULL DEFAULT '',
  claim_idempotency_key TEXT NOT NULL DEFAULT '',
  claim_request_hash TEXT NOT NULL DEFAULT '',
  revocation_receipt_id TEXT NOT NULL DEFAULT '',
  revocation_receipt_hash TEXT NOT NULL DEFAULT '',
  revocation_receipt_salt TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS control_plane_remote_pairings_capacity_key
  ON control_plane_remote_pairings (state, reservation_expires_at, seat_released, id);

CREATE TABLE IF NOT EXISTS control_plane_remote_device_credentials (
  id TEXT PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  pairing_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  role TEXT NOT NULL,
  credential_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  provider_user_id TEXT NOT NULL DEFAULT '',
  issued_at TEXT NOT NULL,
  revoked_at TEXT NOT NULL DEFAULT '',
  issued_idempotency_key TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS control_plane_remote_device_credentials_pair_device_key
  ON control_plane_remote_device_credentials (pairing_id, device_id);

CREATE INDEX IF NOT EXISTS control_plane_remote_device_credentials_pair_status_key
  ON control_plane_remote_device_credentials (pairing_id, status);

ALTER TABLE control_plane_remote_invitations ADD COLUMN IF NOT EXISTS label TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_invitations ADD COLUMN IF NOT EXISTS idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS desktop_device_id TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS desktop_device_label TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS ios_device_id TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS ios_device_label TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS create_idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS create_request_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS claim_idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS claim_request_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS revocation_receipt_id TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS revocation_receipt_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_pairings ADD COLUMN IF NOT EXISTS revocation_receipt_salt TEXT NOT NULL DEFAULT '';
ALTER TABLE control_plane_remote_device_credentials ADD COLUMN IF NOT EXISTS issued_idempotency_key TEXT NOT NULL DEFAULT '';
