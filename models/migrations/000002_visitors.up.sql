CREATE TABLE visitors (
  uuid UUID PRIMARY KEY,
  ip_address TEXT NOT NULL,
  user_agent TEXT,
  accept_language TEXT,
  referer TEXT,
  host TEXT,
  forwarded_for TEXT,
  raw_headers TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE chat_threads
  ADD COLUMN visitor_uuid UUID REFERENCES visitors(uuid) ON DELETE SET NULL;

CREATE INDEX chat_threads_visitor_uuid_idx
  ON chat_threads (visitor_uuid);
