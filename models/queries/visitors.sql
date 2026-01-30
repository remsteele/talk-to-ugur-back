-- name: CreateVisitor :one
INSERT INTO visitors (
  uuid,
  ip_address,
  user_agent,
  accept_language,
  referer,
  host,
  forwarded_for,
  raw_headers
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING *;

-- name: GetVisitor :one
SELECT * FROM visitors
WHERE uuid = $1;

-- name: UpdateVisitorLastSeen :one
UPDATE visitors
SET
  last_seen_at = now(),
  ip_address = $2,
  user_agent = $3,
  accept_language = $4,
  referer = $5,
  host = $6,
  forwarded_for = $7,
  raw_headers = $8
WHERE uuid = $1
RETURNING *;
