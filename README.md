# Codex Claude Bridge

Anthropic-compatible facade over ChatGPT Codex Responses, written in Go.

## Why this bridge exists

Claude Code currently has the best background-task UX for this workflow:

- You can keep chatting while background agents/tasks continue running.
- You can stop specific background agents/tasks without stopping everything.
- It works well for orchestrating multiple concurrent coding threads.

This bridge keeps that Claude Code experience while routing model execution to Codex.

I could not find another adapter that lets you use a Codex subscription through the Claude Code interface, so this repo is public and free to fork.

I plan to actively maintain this project and keep shipping bug fixes. If someone builds a better adapter, that is welcome too.

## Quickstart (Claude + bridge)

```bash
export PORT=8083
export CODEX_AUTH_JSON="${HOME}/.codex/auth.json"
go run ./cmd/server
```

In another terminal, point Claude at the bridge:

```bash
export ANTHROPIC_BASE_URL="http://127.0.0.1:${PORT}"
# optional, only if PROXY_API_KEY is set on the bridge:
export ANTHROPIC_API_KEY="optional-shared-secret"
claude
```

## Endpoints

- `POST /v1/messages`
- `POST /v1/messages` with `"stream": true` (SSE)
- `POST /v1/messages/count_tokens` (deterministic approximation)
- `GET /healthz`

## Behavior

- Requires `anthropic-version` header on `/v1/messages` and `/v1/messages/count_tokens`.
- Requires `x-api-key` only if `PROXY_API_KEY` is set.
- Any inbound model containing `haiku` maps to `HAIKU_MODEL` (default `gpt-5.1-codex-mini`).
- Other inbound models containing `claude` map to `DEFAULT_CODEX_MODEL`.

## Configuration

- `PORT` (default: `8083`)
- `CODEX_AUTH_JSON` (default: `~/.codex/auth.json`)
- `DEFAULT_CODEX_MODEL` (default: `gpt-5.3-codex`)
- `HAIKU_MODEL` (default: `gpt-5.1-codex-mini`)
- `OPENAI_BASE_URL` (default: `https://chatgpt.com/backend-api/codex`)
- `OPENAI_RESPONSES_PATH` (default: `/responses`)
- `PROXY_API_KEY` (optional)
- `DEBUG_JSON` (default: `false`, enables stdout debug logs)
- `DEBUG_JSON_MAX_LEN` (default: `0`, no truncation)
- `DEBUG_JSONL_PATH` (optional, append structured JSONL logs)

Notes:

- The bridge reads `tokens.access_token` from `CODEX_AUTH_JSON`.
- `DEBUG_JSONL_PATH` writes JSONL logs even when `DEBUG_JSON=false`.
- No `response.json` file is written; JSONL is the only file-based debug log.

## Run bridge only

```bash
export PORT=8083
export CODEX_AUTH_JSON="${HOME}/.codex/auth.json"
export PROXY_API_KEY="optional-shared-secret" # optional
go run ./cmd/server
```

## Example request

```bash
curl -sS http://localhost:8083/v1/messages \
  -H 'content-type: application/json' \
  -H 'anthropic-version: 2023-06-01' \
  -H 'x-api-key: optional-shared-secret' \
  -d '{
    "model": "gpt-5.1-codex",
    "max_tokens": 512,
    "messages": [
      {
        "role": "user",
        "content": [{"type": "text", "text": "Write a tiny Go function that reverses a string."}]
      }
    ]
  }' | jq
```

## JSONL debug examples

```bash
tail -f /tmp/bridge-debug.jsonl | jq -c .
tail -f /tmp/bridge-debug.jsonl | jq -c 'select(.prefix=="upstream.stream.event")'
tail -f /tmp/bridge-debug.jsonl | jq -c 'select(.source=="openai") | .payload.type?'
```
