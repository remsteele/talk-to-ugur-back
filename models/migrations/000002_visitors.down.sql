DROP INDEX IF EXISTS chat_threads_visitor_uuid_idx;
ALTER TABLE chat_threads DROP COLUMN IF EXISTS visitor_uuid;
DROP TABLE IF EXISTS visitors;
