-- SPDX-License-Identifier: Apache-2.0

ALTER TABLE accounts ADD COLUMN provisioning_rollback_hash BLOB
  CHECK(provisioning_rollback_hash IS NULL OR length(provisioning_rollback_hash) = 32);
ALTER TABLE accounts ADD COLUMN provisioning_expires_at INTEGER;
ALTER TABLE accounts ADD COLUMN provisioning_sealed_at INTEGER;

-- Pairing v2 stores only a PSK-derived verifier plus the joiner's authenticated
-- transcript proof. The raw out-of-band PSK never reaches the relay.
ALTER TABLE pairings ADD COLUMN pending_join_proof BLOB
  CHECK(pending_join_proof IS NULL OR length(pending_join_proof) = 32);
ALTER TABLE pairings ADD COLUMN rollback_hash BLOB
  CHECK(rollback_hash IS NULL OR length(rollback_hash) = 32);
ALTER TABLE pairings ADD COLUMN claim_expires_at INTEGER;
ALTER TABLE pairings ADD COLUMN ready_at INTEGER;
ALTER TABLE pairings ADD COLUMN ready_expires_at INTEGER;
ALTER TABLE pairings ADD COLUMN finalized_at INTEGER;

CREATE TABLE IF NOT EXISTS device_rollback_tombstones (
  account_id TEXT NOT NULL,
  device_id TEXT NOT NULL,
  pairing_id TEXT NOT NULL,
  rollback_hash BLOB NOT NULL CHECK(length(rollback_hash) = 32),
  PRIMARY KEY(account_id, device_id, pairing_id)
);

CREATE TABLE IF NOT EXISTS account_rollback_tombstones (
  account_id TEXT PRIMARY KEY,
  rollback_hash BLOB NOT NULL CHECK(length(rollback_hash) = 32)
);

-- The first public cross-device slice is deliberately bounded to two active
-- devices.  A single live reservation prevents concurrent joiners from
-- creating divergent trust rosters; claimed rows remain immutable audit state.
CREATE UNIQUE INDEX IF NOT EXISTS pairings_one_live_reservation_per_account
  ON pairings(account_id)
  WHERE state IN ('created', 'joined', 'approved');
CREATE UNIQUE INDEX IF NOT EXISTS pairings_device_identity_once
  ON pairings(new_device_id)
  WHERE new_device_id IS NOT NULL;

-- Accounts created by pre-v2 clients were already durable.  Mark them sealed
-- without manufacturing a rollback capability.
UPDATE accounts SET provisioning_sealed_at = created_at
WHERE provisioning_sealed_at IS NULL;
