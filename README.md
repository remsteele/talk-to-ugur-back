# talk-to-ugur-back

Go backend so you can talk to Ugur. Stores visitors, chat threads, and messages in Postgres via sqlc and uses OpenAI for replies. 

## Project layout

- `ai/` — OpenAI client + structured output handling
- `config/` — env config
- `models/` — migrations + sqlc queries + generated code
- `prompts/` — system prompt file (hot‑loaded)
- `web/` — HTTP server + handlers

## Environment setup

This app loads `.env` automatically at startup.

1. Copy the example file:

```
cp .env.example .env
```

2. Copy the prompt template:

```
cp prompts/system.txt.example prompts/system.txt
```

3. Fill in at least:

```
OPENAI_API_KEY=your_key_here
POSTGRES_CONN_STR=postgres://postgres:postgres@localhost:5433/talk_to_ugur?sslmode=disable
```

Optional OpenAI overrides:

```
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o-mini
OPENAI_TEMPERATURE=0.7
```

## Prompts

- Default prompt file: `prompts/system.txt`
- You can edit it without restarting the server (read on each request).
- If the file is missing, `AI_SYSTEM_PROMPT` from the environment is used.

Config:

```
AI_SYSTEM_PROMPT_PATH=./prompts/system.txt
AI_SYSTEM_PROMPT=...fallback text...
```

## Emotions (no images)

- The AI returns an `emotion` **string** chosen from `AI_EMOTIONS`.
- The frontend should map that string to whatever UI you want (emoji, badge, CSS class, etc.).

Example:

```
AI_EMOTIONS=neutral,happy,sad,angry,confused,amused,thoughtful,excited
```

## Rate limiting / abuse protection

Requests are rate limited per IP address to reduce abuse.

Configure in `.env`:

```
RATE_LIMIT_ENABLED=true
RATE_LIMIT_REQUESTS=60
RATE_LIMIT_WINDOW_SECONDS=60
RATE_LIMIT_BURST=10
RATE_LIMIT_MAX_STRIKES=5
RATE_LIMIT_BLOCK_SECONDS=600
```

## Running locally (no Docker)

1. Ensure Postgres is running.
2. Run the server:

```
go run ./
```

Migrations run on startup.

If you change SQL files in `models/queries/` or `models/migrations/`, regenerate sqlc code:

```
sqlc generate -f models/sqlc.yaml
```

## Docker

Build + run:

```
docker compose build
docker compose up
```

The Postgres container is exposed on host port `5433` (so it won't clash with a local Postgres on `5432`).

Host connection string:

```
postgres://postgres:postgres@localhost:5433/talk_to_ugur?sslmode=disable
```

## API

### `POST /api/v1/visitors`
Creates a visitor record based on request headers + IP.

Response:
```json
{
  "visitor_id": "uuid"
}
```

Store this `visitor_id` on the client and send it back on future requests:

- Header: `X-Visitor-Id: <uuid>` (recommended), or
- JSON body field: `visitor_id`

The server also sets a `visitor_id` cookie and an `X-Visitor-Id` response header for convenience.

### `POST /api/v1/chat/messages`

Request:
```json
{
  "thread_id": "optional-uuid",
  "visitor_id": "optional-uuid",
  "message": "Hey Ugur, what's up?"
}
```

Response:
```json
{
  "visitor_id": "uuid",
  "thread_id": "uuid",
  "user_message": {
    "id": "uuid",
    "role": "user",
    "content": "Hey Ugur, what's up?",
    "created_at": "2026-01-30T12:34:56Z"
  },
  "assistant_message": {
    "id": "uuid",
    "role": "assistant",
    "content": "...",
    "emotion": "neutral",
    "created_at": "2026-01-30T12:34:57Z"
  }
}
```

#### Streaming

Add `?stream=true` to stream the assistant response via SSE:

```
POST /api/v1/chat/messages?stream=true
```

SSE event types:

- `meta` (JSON) — includes `visitor_id`, `thread_id`, `user_message`, and `emotion`
- `token` (text) — reply text chunks only
- `done` (JSON) — includes `assistant_message`
- `error` (text) — error string

### `GET /api/v1/chat/threads/:thread_id/messages?limit=100`

Returns the stored messages for a thread.

## OpenAI request format (structured output)

Requests use the OpenAI chat completions API with JSON schema output:

```
POST https://api.openai.com/v1/chat/completions
Authorization: Bearer ${OPENAI_API_KEY}
Content-Type: application/json

{
  "model": "gpt-4o-mini",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ],
  "stream": false,
  "response_format": {
    "type": "json_schema",
    "json_schema": {
      "name": "chat_reply",
      "strict": true,
      "schema": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "reply": { "type": "string" },
          "emotion": { "type": "string" }
        },
        "required": ["reply", "emotion"]
      }
    }
  }
}
```
