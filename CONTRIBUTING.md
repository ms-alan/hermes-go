# Contributing to hermes-go

Thank you for your interest in contributing!

## Development Setup

```bash
git clone https://github.com/ms-alan/hermes-go.git
cd hermes-go
go mod download
go build ./...
go test ./...
```

Go 1.24+ is required. Set `GOROOT` if using Homebrew-installed Go:

```bash
export GOROOT=/opt/homebrew/Cellar/go/1.24.3/libexec
```

## Project Layout

| Directory | Purpose |
|-----------|---------|
| `cmd/hermes/` | CLI REPL entry point |
| `cmd/gateway/` | QQBot gateway + HTTP API |
| `pkg/agent/` | AIAgent + SessionAgent |
| `pkg/model/` | LLM provider interface |
| `pkg/tools/` | Tool registry + built-in tools |
| `pkg/session/` | SQLite FTS5 persistence |
| `pkg/context/` | Context window management |
| `pkg/gateway/` | PlatformAdapter interface |
| `pkg/config/` | Config loading (YAML/JSON + env) |

## Code Standards

- Format: `gofmt` — run `gofmt -w .` before committing
- Vet: `go vet ./...` — no errors
- Tests: `go test -race ./...` — all pass
- Naming: Go conventions (`camelCase` for exported, `PascalCase` for unexported)

## Commit Convention

Use clear, concise commit messages:

```
feat: add MiniMax client implementation
fix: resolve infinite recursion in config resolveSecretsWalk
docs: update README with HTTP API example
refactor: extract RegisterBuiltinToolsToAgent to pkg/tools
```

Prefixes: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `ci`

## Pull Requests

1. Fork the repo and create a branch from `main`
2. Make your changes (add tests if adding functionality)
3. Ensure `go build ./... && go vet ./... && go test ./...` pass
4. Open a PR against `NousResearch/hermes-go:main`

## Reporting Issues

- Search existing issues first
- Include: Go version, OS, minimal reproduction steps
- Tag appropriately: `bug`, `enhancement`, `question`
