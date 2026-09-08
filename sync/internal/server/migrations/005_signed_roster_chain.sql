-- SPDX-License-Identifier: Apache-2.0
-- Only finalized signed checkpoints are public to existing authenticated peers.
CREATE TABLE account_rosters (
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  version INTEGER NOT NULL CHECK(version >= 1 AND version < 128),
  digest BLOB NOT NULL CHECK(length(digest) = 32),
  roster_json BLOB NOT NULL,
  PRIMARY KEY(account_id, version),
  UNIQUE(account_id, digest)
);
ALTER TABLE pairings ADD COLUMN base_roster_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pairings ADD COLUMN base_roster_hash BLOB
  CHECK(base_roster_hash IS NULL OR length(base_roster_hash) = 32);
