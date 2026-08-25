-- SPDX-License-Identifier: Apache-2.0

-- GC retains the newest envelope for every opaque object written by every
-- device. This covering prefix makes the correlated frontier lookup bounded
-- without revealing phrase text or any other plaintext routing field.
CREATE INDEX IF NOT EXISTS envelopes_account_device_object_cursor_idx
  ON envelopes(account_id, device_id, object_id, cursor DESC);
