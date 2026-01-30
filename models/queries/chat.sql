-- name: CreateChatThread :one
INSERT INTO chat_threads (uuid, visitor_uuid)
VALUES ($1, $2)
RETURNING *;

-- name: GetChatThread :one
SELECT * FROM chat_threads
WHERE uuid = $1;

-- name: CreateChatMessage :one
INSERT INTO chat_messages (uuid, thread_uuid, role, content, emotion)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetChatMessagesByThread :many
SELECT * FROM chat_messages
WHERE thread_uuid = $1
ORDER BY created_at ASC;

-- name: GetChatMessagesByThreadLimit :many
SELECT * FROM chat_messages
WHERE thread_uuid = $1
ORDER BY created_at DESC
LIMIT $2;
