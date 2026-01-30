# talk-to-ugur-back

Go backend so you too can talk to Ugur. Stores visitors, chat threads, and messages in Postgres via sqlc and calls DeepSeek for replies.

## Project layout

- `ai/` — DeepSeek client and JSON reply parsing
- `assets/emotions/` — emotion images (served at `/emotions/...`)
- `prompts/system.txt` — editable system prompt (loaded on each request)
- `models/` — migrations + sqlc queries + generated code
- `web/` — HTTP server + handlers

## Environment setup

This app loads `.env` automatically at startup.

1. Copy the example file:

```
cp .env.example .env
```

2. Fill in at least:

```
DEEPSEEK_API_KEY=your_key_here
POSTGRES_CONN_STR=postgres://postgres:postgres@localhost:5432/talk_to_ugur?sslmode=disable
```

## Prompts

- Default prompt file: `prompts/system.txt`
- You can edit this file without restarting the server (it is read on each request).
- If the file is missing, `AI_SYSTEM_PROMPT` from the environment is used.

Config:

```
AI_SYSTEM_PROMPT_PATH=./prompts/system.txt
AI_SYSTEM_PROMPT=...fallback text...
```

## Emotions + images

- The AI returns an `emotion` string from `AI_EMOTIONS`.
- Put your emotion images in `assets/emotions/` with filenames matching the emotion names you want to use, for example:

```
assets/emotions/neutral.png
assets/emotions/happy.png
assets/emotions/angry.png
```

- Static files are served at:
  - `GET /emotions/<filename>` (ex: `/emotions/angry.png`)
  - `GET /assets/...` (full assets directory)

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

Compose mounts `./prompts` and `./assets` into the container so you can update prompts and images without rebuilding.
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

### `GET /api/v1/chat/threads/:thread_id/messages?limit=100`

Returns the stored messages for a thread.

## DeepSeek request format

Requests follow the DeepSeek chat completions API, for example:

```
POST https://api.deepseek.com/chat/completions
Authorization: Bearer ${DEEPSEEK_API_KEY}
Content-Type: application/json

{
  "model": "deepseek-chat",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ],
  "stream": false
}
```
