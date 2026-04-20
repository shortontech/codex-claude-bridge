# Codex Claude Bridge

Clean Go scaffold for an Anthropic-compatible facade over ChatGPT Codex Responses.

Allows you to use Claude Code with the Codex SDK over a performant Go bridge that still likely has bugs.

## What this scaffold includes

- `POST /v1/messages` (Anthropic-shaped request/response)
- `POST /v1/messages` streaming (`stream=true`) with Anthropic-style SSE events (text-delta baseline)
- `POST /v1/messages/count_tokens` (deterministic approximation)
- `GET /healthz`
- Env-based config
- Minimal Anthropic -> OpenAI Responses conversion
- Anthropic-style error payloads
- Baseline tool mapping (`tool_use` / `tool_result` <-> Responses function call items)
- Anthropic header checks:
  - `anthropic-version` is required for `/v1/messages` and `/v1/messages/count_tokens`
  - `x-api-key` is required only if `PROXY_API_KEY` is configured

## Not implemented yet

- Full Anthropic parity for all streaming/tool event edge cases
- Exact token counting parity (current endpoint is approximate)

## Run

```bash
export CODEX_AUTH_JSON="${HOME}/.codex/auth.json"

export PROXY_API_KEY="optional-shared-secret" # optional
export DEFAULT_CODEX_MODEL="gpt-5.1-codex"
go run ./cmd/server
```

Auth mode:
- The bridge reads `tokens.access_token` from `CODEX_AUTH_JSON`.
- Default upstream target is `https://chatgpt.com/backend-api/codex/responses`.
- `OPENAI_BASE_URL` and `OPENAI_RESPONSES_PATH` can override the upstream target.
- Set `DEBUG_JSON=true` to log inbound Anthropic payloads and upstream JSON payloads for debugging.
- Set `DEBUG_JSON_MAX_LEN=0` for no truncation (or a positive number to cap payload size).
- Set `DEBUG_JSONL_PATH=/absolute/path/debug.jsonl` to append structured JSONL logs suitable for `tail | jq`.
- Any inbound Claude model containing `haiku` is remapped to `HAIKU_MODEL` (default `gpt-5.3-codex`).
- Other inbound Claude models are remapped to `DEFAULT_CODEX_MODEL`.

JSONL examples:

```bash
tail -f /tmp/bridge-debug.jsonl | jq -c .
tail -f /tmp/bridge-debug.jsonl | jq -c 'select(.prefix=="upstream.stream.event")'
tail -f /tmp/bridge-debug.jsonl | jq -c 'select(.source=="openai") | .payload.type?'
```

## Request example

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

## Count tokens

`POST /v1/messages/count_tokens` returns an `input_tokens` estimate from request text content.

- This is a deterministic approximation, not an exact Anthropic tokenizer match.
- It includes text from `system`, message roles, and text blocks in `messages[].content[]`.
- Non-text content blocks are ignored.

```bash
curl -sS http://localhost:8083/v1/messages/count_tokens \
  -H 'content-type: application/json' \
  -H 'anthropic-version: 2023-06-01' \
  -H 'x-api-key: optional-shared-secret' \
  -d '{
    "messages": [
      {
        "role": "user",
        "content": [{"type": "text", "text": "Hello from Claude-compatible proxy"}]
      }
    ]
  }' | jq
```
