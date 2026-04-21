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
- `DEFAULT_INSTRUCTIONS` (optional explicit system prompt override)
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
- If `DEFAULT_INSTRUCTIONS` is empty and `CODEX_SYSTEM_PROMPT_URL` is set, the bridge revalidates prompt cache using HTTP `If-Modified-Since` based on the cache file `stat` mtime.
- Prompt resolution runs per request (not only at startup), so upstream prompt changes are picked up during long-running server sessions.
- If the upstream returns `304 Not Modified`, cached prompt content is used.
- If prompt fetch fails, the bridge falls back to cached prompt content (if present), then to `You are a helpful assistant.`.
- Claude harness-specific system lines (billing header/title-generation boilerplate) are stripped before upstream forwarding.
- The bridge composes outbound instructions from a custom base prompt plus memory tails starting at `# auto memory`.
- `CLAUDE.md` is loaded from the project directory (derived from inbound `Primary working directory:`) plus `~/.claude/CLAUDE.md`, cached in-memory per project directory (stable across rotating `cch` values), and only the section from `# auto memory` onward is included.
- Project memory is also loaded from `~/.claude/projects/<encoded-project-path>/memory/MEMORY.md`, where `<encoded-project-path>` is the primary working directory with `/` replaced by `-` (example: `/home/shorton/Documents/claude-code-proxy` -> `-home-shorton-Documents-claude-code-proxy`).
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
```
