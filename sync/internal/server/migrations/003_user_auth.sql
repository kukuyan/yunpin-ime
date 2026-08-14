-- SPDX-License-Identifier: Apache-2.0

-- A YunPin user is an authorization principal, not a cryptographic trust
-- root.  Account/device keys remain client-generated and the relay never sees
-- vocabulary plaintext or recovery roots.
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY CHECK(length(id) = 32),
  username TEXT NOT NULL COLLATE NOCASE UNIQUE CHECK(length(username) BETWEEN 3 AND 64),
  password_hash TEXT NOT NULL CHECK(length(password_hash) BETWEEN 64 AND 512),
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash BLOB PRIMARY KEY CHECK(length(token_hash) = 32),
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS auth_sessions_user_expiry_idx
  ON auth_sessions(user_id, expires_at);

ALTER TABLE accounts ADD COLUMN user_id TEXT REFERENCES users(id);
CREATE INDEX IF NOT EXISTS accounts_user_idx ON accounts(user_id, created_at);
