# Go Rewrite: cursor-agent-acp

An entirely AI generated Agent Client Protocol (ACP) adapter for Cursor CLI, enabling seamless AI-powered coding assistance in ACP-compliant editors like Zed.

> This is a completely "vibe-coded" reimplementation of the original Node.js `cursor-agent-acp` server, written in Go. THIS IS SLOP.

## Why?
I wanted the agent server to be more portable and easier to deploy without Node.js dependencies. Especially in a NixOS setup, like I have, a single Go binary is much easier to manage than an npm package.

## What is included

- ACP JSON-RPC server over stdio (newline-delimited messages, full duplex for client RPC calls)
- Core ACP methods:
  - `initialize`
  - `session/new`, `session/load`, `session/list`, `session/update`, `session/delete`
  - `session/set_mode`, `session/set_model`
  - `session/prompt`, `session/cancel`
  - `session/request_permission`
  - `tools/list`, `tools/call`
- Extension method routing (`_namespace/...`) and notification handling
- Session management with JSON persistence in `sessionDir`
- Cursor CLI bridge (`cursor-agent`) with retries/timeouts
- Prompt notifications (`session/update`) for user/agent/thought chunks
- Slash command registry with dynamic `available_commands_update` notifications:
  - `/model <model-id>`
  - `/plan <text>`
- Tool call lifecycle reporting (`tool_call` / `tool_call_update`)
- Built-in tool providers:
  - Cursor tools: `search_codebase`, `analyze_code`, `apply_code_changes`, `run_tests`, `get_project_info`, `explain_code`
  - ACP filesystem tools (capability-gated): `read_file`, `write_file`
- Auth helpers:
  - `cursor-agent-acp auth login`
  - `cursor-agent-acp auth logout`
  - `cursor-agent-acp auth status`

## Build

```bash
go build ./...
```

Binary entrypoint:

```bash
./cursor-agent-acp
```

Or run directly:

```bash
go run ./cmd/cursor-agent-acp
```

## Validate and start

```bash
# Validate config only
go run ./cmd/cursor-agent-acp --validate

# Start ACP server on stdio
go run ./cmd/cursor-agent-acp
```

## Notes

- Logs are written to `stderr`.
- ACP protocol messages are written to `stdout` only.
- `cursor-agent` CLI must be installed and authenticated for prompt execution.
