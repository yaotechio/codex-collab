# codex-collab

> A razor-thin Go MCP server that lets **Claude Code** collaborate with the local **Codex CLI**: Claude acts as architect/reviewer, Codex as the low-level implementer.

[中文](./README.md) / English

Claude takes a request, spins up Codex over multiple **read-only** rounds to debate the approach, finalizes a plan, and — after you confirm — sends Codex to **implement in write mode**, then verifies the result against the plan. The discussion loop is driven by Claude's agent loop; this server only handles *single-round Codex execution + session resume + safety guardrails*.

## Features

- **A single `codex` tool**, with parameters that map directly to the `codex exec` CLI.
- **Session resume** — debates reuse one `session_id` (Codex remembers context); implementation starts a fresh session.
- **Three layers of safety** — write operations require explicit confirmation + a PreToolUse hook forces user approval + read-only by default.
- **Hard round cap** — a per-session debate-round limit prevents Claude↔Codex from burning tokens in an infinite back-and-forth.
- **Single binary, zero external dependencies** — the process is spawned on demand and the Codex child process is terminated on timeout or cancellation.

Cross-platform: pure Go, supports **macOS / Linux / Windows** (amd64 and arm64).

## Requirements

- [Claude Code](https://claude.com/claude-code)
- [Codex CLI](https://github.com/openai/codex) (`codex` on your PATH, already `codex login`'d; on Windows `codex.cmd`/`codex.exe` is resolved via PATH too)
- For the **plugin install (Option A)**: [Node.js](https://nodejs.org/) ≥ 16 (for `npx`) — no Go needed
- For **build from source (Option B)**: [Go](https://go.dev/) ≥ 1.23

## Installation

### Option A — Plugin marketplace (recommended, no build, no Go)

In Claude Code, run:

```
/plugin marketplace add yaotechio/codex-collab
/plugin install codex-collab@yaotechio
```

One step installs the `codex` MCP server, the `/codex-collab` command, and the write-confirmation hook (needs [Node.js](https://nodejs.org/) ≥ 16; first run takes a few seconds).

After installing, **restart Claude Code** (or run `/reload-plugins`) so the MCP server connects.

Update with `/plugin update codex-collab@yaotechio` (or let Claude Code auto-update at startup). To customize, see [Configuration](#configuration).

> The command may appear namespaced as `/codex-collab:codex-collab` in the `/plugin` picker.

### Option B — Build from source (manual)

```bash
git clone git@github.com:yaotechio/codex-collab.git && cd codex-collab
make build      # auto-detects host OS/arch; produces codex-collab.exe on Windows
```

You get the binary `./codex-collab` (`codex-collab.exe` on Windows). Note its **absolute path** — referred to below as `<BIN>`.

To produce binaries for every platform/arch at once:

```bash
make dist        # builds linux/darwin/windows × amd64/arm64 into ./dist
```

> Without make, just `go build -o codex-collab .`, or cross-compile with `GOOS=linux GOARCH=arm64 go build ...`.

Register with Claude Code:

```bash
claude mcp add codex-collab -s user \
  -e CODEX_MCP_MAX_ROUNDS=6 \
  -e CODEX_MCP_TIMEOUT=300 \
  -- <BIN>
```

Verify:

```bash
claude mcp list      # shows codex-collab: connected
```

A source install also needs the two guardrails wired up by hand (the plugin install bundles them automatically):

**Write-confirmation hook** — add to `~/.claude/settings.json`; write-mode calls prompt for approval, read-only passes through:

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "mcp__codex-collab__codex",
        "hooks": [ { "type": "command", "command": "<BIN> hook" } ] }
    ]
  }
}
```

**Slash command** — copy `.claude/commands/codex-collab.md` into `~/.claude/commands/` (Windows: `%USERPROFILE%\.claude\commands\`) to use `/codex-collab`.

## Configuration

Environment variables (the plugin sets defaults; to override, export them before launching Claude Code):

| Variable | Default | Description |
|---|---|---|
| `CODEX_MCP_MAX_ROUNDS` | 6 | Max debate rounds per session (hard cap) |
| `CODEX_MCP_TIMEOUT` | 300 | Timeout for a single codex call (seconds) |
| `CODEX_MCP_SESSION_TTL` | 24 | How long a session's round counter is retained (hours) |

## Usage

The common path — one command runs the whole flow:

```
/codex-collab optimize the sort performance of ./src/sort.go
```

Claude will: debate with Codex read-only over multiple rounds → present a **Final Plan** and ask you to confirm → after you confirm, send Codex to write files (the hook prompts for approval again) → verify against the plan and report back.

You can also have Claude call the `codex` tool directly; parameters below.

## `codex` tool reference

### Parameters

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `PROMPT` | string | yes | — | Instruction sent to Codex |
| `cd` | string | no | `.` | Working directory (new sessions only) |
| `sandbox` | string | no | `read-only` | `read-only` / `workspace-write` / `danger-full-access` (new sessions only) |
| `session_id` | string | no | — | Pass to resume a session; omit to create a new one |
| `skip_git_repo_check` | bool | no | false | Allow running outside a git repo |
| `model` | string | no | — | Pick a specific Codex model |
| `return_all_messages` | bool | no | false | Return the full reasoning/tool-call event stream |
| `confirmed` | bool | no | false | Must be true for write mode, otherwise rejected |

> Note: when resuming via `session_id`, `sandbox`/`cd` are locked to the original session — passing them has no effect. This guarantees debate sessions stay read-only.

### Return value (JSON)

| Field | Description |
|---|---|
| `success` | Whether this round succeeded |
| `session_id` | Session id (returned on creation, for later resume) |
| `output` | Codex's final reply / patch; the full event stream when `return_all_messages=true` |
| `round` | Rounds completed in this session |
| `rounds_remaining` | Rounds left |
| `error` | Structured error on failure |

## Development

```bash
go test ./...      # unit tests
go build -o codex-collab .
```

## Acknowledgments

- [GuDaStudio/codexmcp](https://github.com/GuDaStudio/codexmcp) (MIT) — the idea of bridging Claude Code and Codex over MCP; this project is an independent Go reimplementation inspired by it.
- [multica-ai/andrej-karpathy-skills](https://github.com/multica-ai/andrej-karpathy-skills) (MIT) — the coding principles in the collaboration workflow (simplicity first, surgical changes, goal-driven, think before coding) are adapted from it (originally from Andrej Karpathy's observations).

## License

[MIT](./LICENSE)
