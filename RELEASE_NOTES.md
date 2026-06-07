# 0.9.0 Release Notes

Codex Claude Bridge is now stable enough for regular local use as an Anthropic-compatible facade for Claude Code, backed by ChatGPT Codex/OpenAI Responses.

This release is intentionally **0.9.0**, not 1.0. The bridge still piggybacks on Codex CLI credentials from `~/.codex/auth.json`, so the auth story is practical for local users with a Codex subscription but not yet a standalone production-grade login or deployment model.

## Highlights

- Runs Claude Code through a local Anthropic-compatible `/v1/messages` bridge.
- Reuses existing Codex CLI auth instead of requiring a second OpenAI login flow.
- Supports non-streaming and streaming Claude Code message flows, including proper Anthropic-style SSE output.
- Translates Claude Code tool calls to OpenAI Responses function calls.
- Preserves Claude Code's background-task-oriented UX while routing model execution to Codex/OpenAI.
- Maps Claude-style model names to configured Codex models, including a separate haiku/small-model mapping.
- Includes a reference `claude-codex.sh` launcher.

## Initial Development

The first development arc focused on getting a usable bridge rather than a generic router:

- Implemented Anthropic-compatible message, count-token, and health endpoints.
- Added request guards for `anthropic-version`, optional shared-secret auth, and method validation.
- Built request/response translation between Claude Code's Anthropic message format and OpenAI Responses input/output.
- Added tool schema normalization and configurable tool pruning through `config/tool_policy.yaml`.
- Added prompt injection support via `__INJECT_PROMPT__`, with committed bridge instructions in `prompts/bridge_system_prompt.md`.
- Added upstream Codex system prompt fetching and cache fallback behavior.
- Added matrix/debug logging for request correlation across inbound and outbound edges.

## Recent Stabilization

The latest fixes made the bridge much more usable in day-to-day Claude Code sessions:

- Fixed hosted web search mapping so Claude Code `WebSearch` is sent upstream as OpenAI's native `web_search` tool instead of a fake client-side function.
- Restored real streaming behavior by forwarding text deltas as they arrive instead of buffering text until the upstream response completes.
- Removed the raw `[DONE]` trailer from Anthropic-style SSE responses.
- Added regression tests for hosted web search translation and live streaming behavior.
- Added compact cache diagnostics behind `DEBUG=true`, written to `DEBUG_CACHE_PATH` with hashes and byte counts rather than raw prompt text.
- Stripped volatile `x-anthropic-billing-header` system lines before forwarding upstream instructions so Claude Code per-request metadata does not perturb OpenAI cache keys.
- Made cache diagnostics cross-platform by defaulting to the OS temp directory.

## Known Limits

- Auth depends on a local Codex CLI auth file and is not a standalone production auth flow.
- The default upstream is the ChatGPT Codex backend, not a generic OpenAI API key flow.
- Prompt caching behavior is ultimately controlled by the upstream backend; the bridge now keeps stable prefixes stable, but OpenAI/Codex cache hits can still be opportunistic or chunk-limited.
- This is primarily a local developer bridge for Claude Code workflows, not a general hosted proxy service.

## Upgrade Notes

- Start the bridge with `go run ./cmd/server`.
- Launch Claude Code through `./claude-codex.sh` or set `ANTHROPIC_BASE_URL=http://127.0.0.1:8083`.
- Use `DEBUG=true` for compact cache diagnostics when investigating prompt-cache behavior.
- Keep `CODEX_AUTH_JSON` pointed at a valid Codex auth file.
