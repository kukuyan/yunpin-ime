-- SPDX-License-Identifier: Apache-2.0

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS accounts (
  id TEXT PRIMARY KEY,
  recovery_authentication_hash BLOB NOT NULL CHECK(length(recovery_authentication_hash) = 32),
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  name_ciphertext BLOB NOT NULL,
  token_hash BLOB NOT NULL UNIQUE CHECK(length(token_hash) = 32),
  ed25519_public_key BLOB NOT NULL CHECK(length(ed25519_public_key) = 32),
  x25519_public_key BLOB NOT NULL CHECK(length(x25519_public_key) = 32),
  ack_cursor INTEGER NOT NULL DEFAULT 0 CHECK(ack_cursor >= 0),
  created_at INTEGER NOT NULL,
  revoked_at INTEGER,
  UNIQUE(account_id, id)
);

CREATE INDEX IF NOT EXISTS devices_account_idx ON devices(account_id, created_at);

CREATE TABLE IF NOT EXISTS pairings (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  creator_device_id TEXT NOT NULL REFERENCES devices(id),
  secret_hash BLOB NOT NULL CHECK(length(secret_hash) = 32),
  state TEXT NOT NULL CHECK(state IN ('created', 'joined', 'approved', 'claimed')),
  pending_name_ciphertext BLOB,
  pending_ed25519_public_key BLOB,
  pending_x25519_public_key BLOB,
  new_device_id TEXT,
  encrypted_keyring BLOB CHECK(encrypted_keyring IS NULL OR (length(encrypted_keyring) >= 49 AND length(encrypted_keyring) <= 262144)),
  expires_at INTEGER NOT NULL,
  claimed_at INTEGER,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS pairings_account_idx ON pairings(account_id, created_at);

CREATE TABLE IF NOT EXISTS keyrings (
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  epoch INTEGER NOT NULL CHECK(epoch > 0),
  ciphertext BLOB NOT NULL CHECK(length(ciphertext) >= 49 AND length(ciphertext) <= 262144),
  writer_device_id TEXT NOT NULL REFERENCES devices(id),
  created_at INTEGER NOT NULL,
  PRIMARY KEY(account_id, epoch)
);

CREATE TABLE IF NOT EXISTS envelopes (
  cursor INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id TEXT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  device_id TEXT NOT NULL REFERENCES devices(id),
  device_seq INTEGER NOT NULL CHECK(device_seq > 0),
  version INTEGER NOT NULL CHECK(version = 1),
  object_id BLOB NOT NULL CHECK(length(object_id) = 16),
  key_epoch INTEGER NOT NULL CHECK(key_epoch > 0),
  previous_hash BLOB CHECK(previous_hash IS NULL OR length(previous_hash) = 32),
  nonce BLOB NOT NULL CHECK(length(nonce) = 24),
  ciphertext BLOB NOT NULL CHECK(length(ciphertext) >= 528 AND length(ciphertext) <= 524816 AND ((length(ciphertext) - 16) % 512) = 0),
  signature BLOB NOT NULL CHECK(length(signature) = 64),
  record_hash BLOB NOT NULL CHECK(length(record_hash) = 32),
  created_at INTEGER NOT NULL,
  UNIQUE(account_id, device_id, device_seq)
);

CREATE INDEX IF NOT EXISTS envelopes_account_cursor_idx ON envelopes(account_id, cursor);
