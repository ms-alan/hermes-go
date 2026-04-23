# hermes-go Feature Parity with hermes-agent (Python)

> Last updated: 2026-04-23
> Branch: `main`
> Python counterpart: `NousResearch/hermes-agent`

This document tracks the feature gap between hermes-go (Go) and hermes-agent (Python).
Green = implemented, Yellow = partial, Red = not yet.

---

## Implemented ✅

| Feature | Status | Notes |
|---------|--------|-------|
| **Core Agent Loop** | ✅ | `pkg/agent/agent.go` — RunWithMessages, tool calling, iteration limit |
| **Built-in Tools** | ✅ | `pkg/tools/builtin.go` — file_read, file_write, search_files, grep, bash, web_search (Tavily), delegate_task |
| **Session Management** | ✅ | `pkg/session/` — SQLite store, session create/switch/list/delete |
| **Context Compression** | ✅ | `pkg/context/compressor.go` — iterative chunked summarization (MaxLLMSummaryInputTokens) |
| **Graceful Shutdown** | ✅ | `cmd/hermes/main.go` — signal.NotifyContext + stdin close for REPL |
| **Structured Logging** | ✅ | All packages use `log/slog` (Go 1.21+) |
| **MCP Client** | ✅ | `pkg/mcp/` — StdioTransport + HTTPTransport, JSON-RPC 2.0 |
| **MCP Tool Integration** | ✅ | `pkg/tools/mcp_tool.go` — MCP servers register as tools in agent loop |
| **Web Search (Tavily)** | ✅ | Real HTTP API via `TAVILY_API_KEY` env var |
| **Delegate Tool (Subagents)** | ✅ | `pkg/tools/delegate_tool.go` — single + batch mode, depth=1 flat, blocked tools |
| **MiniMax Model Client** | ✅ | `pkg/model/minimax.go` — /v1 base URL, double JSON decode fix |
| **Config (YAML)** | ✅ | `config.Load()` from `~/.hermes/config.yaml` |
| **REPL Interface** | ✅ | `cmd/hermes/` — readline-style REPL with /help, /tools, /sessions |
| **QQBot Gateway** | ✅ | `pkg/gateway/qqbot/` — receives messages and routes to agent |

---

## Partial / Needs Improvement ⚠️

| Feature | Status | Notes |
|---------|--------|-------|
| **Skill Loader** | ⚠️ partial | `pkg/skill/` — loader.go + registry.go exist, but no skillsets-based dynamic loading |
| **Authorization / ToolApproval** | ⚠️ none | `file_write`, `file_delete`, `bash` have no confirmation prompt for dangerous ops |
| **Prompt Builder** | ⚠️ none | No dedicated prompt_builder module; system prompts built inline in agent.go |
| **Graceful Shutdown (gateway)** | ⚠️ partial | Gateway main.go lacks signal handler; REPL graceful shutdown done, gateway not yet |
| **Token Counting** | ⚠️ rough | `pkg/context/token.go` uses character ÷ 4 approximation, not tiktoken |
| **Double-JSON Decode** | ⚠️ in agent | `pkg/agent/agent.go` handles double-encoded tool args (MiniMax API quirk), but only for MiniMax |

---

## Not Yet Implemented ❌

| Feature | Python File | Priority | Notes |
|---------|------------|----------|-------|
| **terminal_tool (multi-backend)** | `tools/terminal_tool.py` | High | Python has local/docker/ssh/modal/singularity/daytona; Go only has simple exec |
| **browser_tool** | `tools/browser_tool.py` | Medium | Browserbase automation — no Go equivalent |
| **context_compressor (full)** | `agent/context_compressor.py` 1276L | Medium | Python has `_prune_old_tool_results`, `redact_sensitive_text`, `focus_topic` compression, structured summary template with 12 sections |
| **delegate_tool (full)** | `tools/delegate_tool.py` 2158L | Medium | Python has orchestrator role, TUI spinner, parent_cb relay, spawn pause, MCP tool preservation, credential override, ACP transport |
| **mcp_tool (full)** | `tools/mcp_tool.py` 2659L | Medium | Python has SSE/HTTP2 transport, reconnection with backoff, sampling support, server-initiated LLM requests |
| **skillsets dynamic loading** | `tools/skill_loader.py` | Medium | Skills as YAML/JSON files with toolsets; `/skills` CLI command |
| **authorization / dangerous command detection** | `tools/authorize.py` | Low | Confirms before rm -rf, git push --force, etc. |
| **linear integration** | `tools/linear_tool.py` | Low | Linear API for issue management |
| **arxiv search** | `tools/arxiv_tool.py` | Low | arXiv paper search |
| **TUI / Interactive Overlay** | `agent/display/` | Low | Rich terminal UI with subagent progress, spinner, color |
| **gateway RPCs** | `hermes_cli/gateway_rpc.py` | Low | `delegation.pause`, `delegation.status`, `subagent.interrupt` |

---

## Go-Specific Issues to Fix

| Issue | Location | Bug |
|-------|----------|-----|
| `WithBaseURL` hardcoded `/anthropic` | `pkg/model/minimax.go:24` | Fixed in commit `d1fc186` |
| `MINIMAX_CN_BASE_URL` env defaulting to `/anthropic` | `~/.hermes/.env:75,402` | Fixed |
| `compressor.go takeChunkByTokens` param type | `pkg/context/compressor.go` | Fixed (Go short-form typing bug) |

---

## Memory Note

> If you edit this file, also update `~/.hermes/memory.md` with the current commit SHA so new sessions know the baseline.

---

## Quick Reference: hermes-go Package Layout

```
cmd/hermes/        # CLI REPL entrypoint
cmd/gateway/       # Gateway service
pkg/agent/         # Core AIAgent loop
pkg/context/       # Token counting + compressor
pkg/gateway/       # QQBot + platform abstraction
pkg/mcp/           # MCP transport (Stdio + HTTP)
pkg/model/         # OpenAI + MiniMax LLM clients
pkg/session/       # SQLite session store
pkg/skill/         # Skill loader + registry
pkg/tools/         # Tool registry + built-in tools
config/            # YAML config
```

## Quick Reference: Python hermes-agent Package Layout

```
hermes_agent/      # Core AIAgent
  agent/           # Context compressor, display, run loop
  tools/           # All tool implementations
  hermes_cli/      # CLI, gateway, config
  model_tools.py   # Tool orchestration
```

---

## Contributing to Parity

When implementing a missing feature:

1. Create a branch from `main`
2. Implement the Go version
3. Add tests in `<package>_test.go`
4. Update this document (move from ❌ to ⚠️ or ✅)
5. Open a PR to `ms-alan/hermes-go`
