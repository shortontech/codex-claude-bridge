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
- Any inbound model containing `haiku` maps to `HAIKU_MODEL` (default `gpt-5.3-codex-spark`).
- Other inbound models containing `claude` map to `DEFAULT_CODEX_MODEL`.

## Configuration

- `PORT` (default: `8083`)
- `CODEX_AUTH_JSON` (default: `~/.codex/auth.json`)
- `DEFAULT_CODEX_MODEL` (default: `gpt-5.3-codex`)
- `HAIKU_MODEL` (default: `gpt-5.3-codex-spark`)
- `DEFAULT_INSTRUCTIONS` (optional explicit system prompt override)
- `BRIDGE_SYSTEM_PROMPT_FILE` (default: `prompts/bridge_system_prompt.md`)
- `TOOL_POLICY_FILE` (default: `config/tool_policy.yaml`)
- `CODEX_SYSTEM_PROMPT_URL` (default: `https://raw.githubusercontent.com/openai/codex/main/codex-rs/models-manager/prompt.md`)
- `CODEX_SYSTEM_PROMPT_CACHE` (default: `codex_system_prompt.txt` in current working directory)
- `OPENAI_BASE_URL` (default: `https://chatgpt.com/backend-api/codex`)
- `OPENAI_RESPONSES_PATH` (default: `/responses`)
- `PROXY_API_KEY` (optional)
- `DEBUG_JSON` (default: `false`, enables stdout debug logs)
- `DEBUG_JSON_MAX_LEN` (default: `0`, no truncation)
- `DEBUG_JSONL_PATH` (optional, append structured JSONL logs)

Notes:

- The bridge reads `tokens.access_token` from `CODEX_AUTH_JSON`.
- The bridge default system prompt comes from `prompts/bridge_system_prompt.md` (or `BRIDGE_SYSTEM_PROMPT_FILE`).
- To inject that default into Claude Code custom system prompts, include `__INJECT_PROMPT__` in `--system-prompt`; the bridge replaces the marker with the committed default prompt.
- The bridge prunes and rewrites tool metadata using `config/tool_policy.yaml` (or `TOOL_POLICY_FILE`) before forwarding tools upstream.
- If `DEFAULT_INSTRUCTIONS` is empty and `CODEX_SYSTEM_PROMPT_URL` is set, the bridge revalidates prompt cache using HTTP `If-Modified-Since` based on the cache file `stat` mtime.
- Prompt resolution runs per request (not only at startup), so upstream prompt changes are picked up during long-running server sessions.
- If the upstream returns `304 Not Modified`, cached prompt content is used.
- If prompt fetch fails, the bridge falls back to cached prompt content (if present), then to `You are a helpful assistant.`.
- The bridge forwards inbound message content without stripping harness-specific reminder/notification blocks.
- Outbound instructions use the bridge base instructions plus `Primary working directory` when present in the inbound system context.
- Matrix logs are emitted under `prefix="matrix"` with `request_id`, `edge`, and `event` to correlate the four request/response directions.
- Matrix logs include explicit `rejected`/`drop` events for swallowed paths (guard failures, malformed upstream SSE events, unknown event types, and unmapped tool output indexes).
- The bridge returns `x-request-id` on `/v1/messages` responses for end-to-end log correlation.
- The bridge logs cache invariants to stderr as `[cache] ... cached_tokens=...` for both streaming and non-streaming responses.
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
tail -f /tmp/bridge-debug.jsonl | jq -c 'select(.prefix=="matrix") | {request_id:.payload.request_id,edge:.payload.edge,event:.payload.event}'
```
