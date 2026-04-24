# hermes-go Feature Parity with hermes-agent (Python)

> Last updated: 2026-04-24
> Branch: `main` (commit fb8ac3b)
> Python counterpart: `NousResearch/hermes-agent`

This document tracks the feature gap between hermes-go (Go) and hermes-agent (Python).
Green = implemented, Yellow = partial, Red = not yet.

---

## Implemented ✅

| Feature | Status | Notes |
|---------|--------|-------|
| **Core Agent Loop** | ✅ | `pkg/agent/agent.go` — RunWithMessages, tool calling, iteration limit |
| **Built-in Tools** | ✅ | `pkg/tools/builtin.go` — file_read, file_write, file_delete, terminal, web_search, delegate_task |
| **Dangerous Command Authorization** | ✅ | `pkg/tools/approval.go` — wired into terminal/file_write/file_delete handlers |
| **Session Management** | ✅ | `pkg/session/` — SQLite store, session create/switch/list/delete |
| **Context Compression** | ✅ | `pkg/context/compressor.go` — full parity: `_prune_tool_results` (hash dedup), `redact_sensitive_text` (14+ regex patterns), `focus_topic`, `summarize_messages`, 13-section structured summary template |
| **Graceful Shutdown (REPL)** | ✅ | `cmd/hermes/main.go` — signal.NotifyContext + stdin close |
| **Structured Logging** | ✅ | All packages use `log/slog` (Go 1.21+) |
| **MCP Client (Stdio+HTTP+SSE)** | ✅ | `pkg/mcp/` — client.go + sse_transport.go + types.go + config.go + HTTP2 transport + exponential backoff reconnection |
| **MCP Tool Integration** | ✅ | `pkg/tools/mcp_tool.go` — init() calls initMCPServers() on load; HTTP2 multiplexing, SSE with exponential backoff reconnection (1s→30s, 2x factor, jitter), SamplingHandler for server-initiated LLM requests |
| **Web Search (4 backends)** | ✅ | `pkg/tools/web_search.go` — Exa + Tavily + DuckDuckGo + Firecrawl |
| **Delegate Tool (Subagents)** | ✅ | `pkg/tools/delegate_tool.go` + `pkg/agent/delegate.go` — single + batch mode, orchestrator role, spawn pause/interrupt, progress callback, maxSpawnDepth |
| **Skillsets Hub** | ✅ | `pkg/skill/` — loader.go + registry.go + REPL /skills command |
| **MiniMax Model Client** | ✅ | `pkg/model/minimax.go` — /v1 base URL, double JSON decode fix |
| **Tool Arg Parsing** | ✅ | `pkg/agent/agent.go` — `parseToolArgs()` helper handles single, double, and triple encoding (max depth 3) |
| **Config (YAML)** | ✅ | `config.Load()` from `~/.hermes/config.yaml` |
| **REPL Interface** | ✅ | `cmd/hermes/` — /help, /tools, /sessions, /skills, /new, /switch |
| **Memory System** | ✅ | `pkg/memory/` — MemoryManager + BuiltinMemoryProvider + pluggable provider arch |
| **Cron Scheduler** | ✅ | `pkg/cron/` — scheduler.go + cron_tool.go + runner.go + deliverer.go |
| **QQ Push via Cron** | ✅ | `pkg/cron/deliverer.go` — QQDeliverer + AicallRunner wired in gateway main.go |
| **Browser Automation** | ✅ | `pkg/tools/browser/browser.go` — chromedp multi-tab, screenshot, annotate, click/type/scroll |
| **Terminal Multi-backend** | ✅ | `pkg/terminal/backend.go` — local + docker + ssh + singularity + modal + daytona (837 lines) |
| **Code Execution Sandbox** | ✅ | `pkg/tools/code_execution.go` — UDS RPC + hermes_tools.py stub, 7 allowed tools |
| **Web Extract** | ✅ | `pkg/tools/web_extract_tool.go` — HTML stripping + proxy support |
| **Image Generation** | ✅ | `pkg/tools/image_gen_tool.go` — MiniMax API, base64 or URL download |
| **Mixture of Agents** | ✅ | `pkg/tools/mixture_of_agents_tool.go` |
| **gateway QQ Bot** | ✅ | `pkg/gateway/` — PlatformAdapter + QQBot integration |
| **Linear Integration** | ✅ | `pkg/tools/linear_tool.go` — GraphQL client: list/create/update/search issues, teams, workflow states, labels |

---

## Partial / Needs Improvement ⚠️

| Feature | Status | Notes |
|---------|--------|-------|
| **Token Counting** | ✅ | `pkg/context/token.go` — tiktoken cl100k_base (same as GPT-4/GPT-3.5), lazy-init with fallback to character heuristic, `CountTokensForModel` per-model encoding support |

---

## Not Yet Implemented ❌

| Feature | Python File | Priority | Notes |
|---------|------------|----------|-------|
| **arxiv search** | `tools/arxiv_tool.py` | Low | arXiv paper search |
| **TUI / Interactive Overlay** | `agent/display/` | Low | Rich terminal UI with subagent progress, spinner, color |
| **gateway RPCs** | `hermes_cli/gateway_rpc.py` | Low | `delegation.pause`, `delegation.status`, `subagent.interrupt` |
| **authorization / dangerous command detection** | `tools/authorize.py` | Low | Confirms before rm -rf, git push --force, etc. |

---

## Quick Reference: hermes-go Package Layout

```
cmd/hermes/        # CLI REPL entrypoint
cmd/gateway/       # Gateway service (QQ bot + cron scheduler)
pkg/agent/         # Core AIAgent loop + delegate
pkg/context/       # Token counting + compressor
pkg/cron/          # Scheduler + runner + deliverer (QQ push)
pkg/gateway/       # QQBot + platform abstraction
pkg/mcp/           # MCP transport (Stdio + HTTP + SSE)
pkg/memory/        # MemoryManager + MemoryProvider plugin arch
pkg/model/         # OpenAI + MiniMax LLM clients
pkg/prompt/        # System prompt Builder (slot-based composition)
pkg/session/       # SQLite session store
pkg/skill/         # Skill loader + registry
pkg/terminal/      # Multi-backend terminal (local/docker/ssh/singularity/modal/daytona)
pkg/tools/         # Tool registry + built-in tools + browser automation
config/            # YAML config
```

---

## Contributing to Parity

When implementing a missing feature:

1. Create a branch from `main`
2. Implement the Go version
3. Add tests in `<package>_test.go`
4. Update this document (move from ❌ to ⚠️ or ✅)
5. Open a PR to `ms-alan/hermes-go`
