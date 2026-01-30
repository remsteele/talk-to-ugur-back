CREATE TABLE chat_threads (
  uuid UUID PRIMARY KEY,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE chat_messages (
  uuid UUID PRIMARY KEY,
  thread_uuid UUID NOT NULL REFERENCES chat_threads(uuid) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
  content TEXT NOT NULL,
  emotion TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX chat_messages_thread_uuid_created_at_idx
  ON chat_messages (thread_uuid, created_at);
