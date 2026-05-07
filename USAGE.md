# Usage

## Prerequisites

Before starting, make sure the machine has:

- Go installed and available as `go`.
- Claude Code installed and available as `claude`.
- A valid Codex auth file at `${HOME}/.codex/auth.json`.
- This repository checked out locally.

Run bridge commands from the repository root unless noted otherwise.

## Start The Bridge

Run the bridge first:

```bash
export PORT=8083
export CODEX_AUTH_JSON="${HOME}/.codex/auth.json"
go run ./cmd/server
```

Then start Claude Code through the bridge.

## Recommended Launcher

This repo includes a reference launcher at `claude-codex.sh`:

```bash
./claude-codex.sh
```

You can also install or symlink it somewhere in `PATH` as `claude-codex`, then use it from the workspace where you want Claude Code to run:

```bash
claude-codex
```

The reference launcher is:

```bash
#!/bin/bash
export CLAUDE_CODE_NO_FLICKER=1
export CLAUDE_CODE_DISABLE_MOUSE=1
export ANTHROPIC_BASE_URL=http://127.0.0.1:8083
export CLAUDE_CODE_OAUTH_TOKEN="$CLAUDE_CODE_OAUTH_TOKEN"
export CLAUDE_CODE_ENABLE_TELEMETRY=0
export DISABLE_TELEMETRY=1
claude --model gpt-5.5 --system-prompt __INJECT_PROMPT__ "$@"
```

`__INJECT_PROMPT__` is replaced by the bridge with `prompts/bridge_system_prompt.md` unless `BRIDGE_SYSTEM_PROMPT_FILE` points somewhere else.

## Manual Launch

Without the launcher, set Claude Code's Anthropic-compatible endpoint yourself:

```bash
export ANTHROPIC_BASE_URL="http://127.0.0.1:8083"
claude --model gpt-5.5 --system-prompt __INJECT_PROMPT__
```

If the bridge is started with `PROXY_API_KEY`, also set:

```bash
export ANTHROPIC_API_KEY="optional-shared-secret"
```

## Notes

- `claude-codex` assumes the bridge is listening on `127.0.0.1:8083`.
- `claude-codex.sh` is intended as a copyable reference script for local installs.
- The launcher model can be changed by editing the launcher or by running Claude Code manually.
- Keep the bridge running for the duration of the Claude Code session.
