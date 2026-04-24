# hermes-go Feature Parity with hermes-agent (Python)

> Last updated: 2026-04-24
> Branch: `main` (commit 93fea96)
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
| **Context Compression** | ✅ | `pkg/context/compressor.go` — iterative chunked summarization |
| **Graceful Shutdown (REPL)** | ✅ | `cmd/hermes/main.go` — signal.NotifyContext + stdin close |
| **Structured Logging** | ✅ | All packages use `log/slog` (Go 1.21+) |
| **MCP Client (Stdio+HTTP+SSE)** | ✅ | `pkg/mcp/` — client.go + sse_transport.go + types.go + server.go + config.go |
| **MCP Tool Integration** | ✅ | `pkg/tools/mcp_tool.go` — init() calls initMCPServers() on load |
| **Web Search (4 backends)** | ✅ | `pkg/tools/web_search.go` — Exa + Tavily + DuckDuckGo + Firecrawl |
| **Delegate Tool (Subagents)** | ✅ | `pkg/tools/delegate_tool.go` + `pkg/agent/delegate.go` — single + batch mode, depth=1 flat |
| **Skillsets Hub** | ✅ | `pkg/skill/` — loader.go + registry.go + REPL /skills command |
| **MiniMax Model Client** | ✅ | `pkg/model/minimax.go` — /v1 base URL, double JSON decode fix |
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

---

## Partial / Needs Improvement ⚠️

| Feature | Status | Notes |
|---------|--------|-------|
| **Skill Loader (dynamic)** | ⚠️ partial | `pkg/skill/` — loader.go + registry.go exist, skillsets YAML loading in progress |
| **Prompt Builder** | ⚠️ none | No dedicated prompt_builder module; system prompts built inline in agent.go |
| **Graceful Shutdown (gateway)** | ⚠️ partial | Gateway main.go lacks signal handler; REPL graceful shutdown done |
| **Token Counting** | ⚠️ rough | `pkg/context/token.go` uses character ÷ 4 approximation, not tiktoken |
| **context_compressor (full)** | ⚠️ partial | Python has `_prune_old_tool_results`, `redact_sensitive_text`, `focus_topic` + 12-section summary template |
| **delegate_tool (full)** | ⚠️ partial | Missing: orchestrator role, TUI spinner, parent_cb relay, spawn pause, MCP tool preservation |
| **mcp_tool (full)** | ⚠️ partial | Missing: HTTP2, reconnection with backoff, sampling support, server-initiated LLM requests |
| **Double-JSON Decode** | ⚠️ in agent | `pkg/agent/agent.go` handles double-encoded tool args for MiniMax, not generic |

---

## Not Yet Implemented ❌

| Feature | Python File | Priority | Notes |
|---------|------------|----------|-------|
| **linear integration** | `tools/linear_tool.py` | Low | Linear API for issue management |
| **arxiv search** | `tools/arxiv_tool.py` | Low | arXiv paper search |
| **TUI / Interactive Overlay** | `agent/display/` | Low | Rich terminal UI with subagent progress, spinner, color |
| **gateway RPCs** | `hermes_cli/gateway_rpc.py` | Low | `delegation.pause`, `delegation.status`, `subagent.interrupt` |
| **authorization / dangerous command detection** | `tools/authorize.py` | Low | Confirms before rm -rf, git push --force, etc. |
| **skill dynamic loading (full)** | `tools/skill_loader.py` | Medium | Skills as YAML/JSON files with toolsets; /skills CLI with hot-reload |

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
