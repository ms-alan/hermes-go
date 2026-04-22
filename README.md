# Hermes Agent (Go Version)

[![CI](https://github.com/ms-alan/hermes-go/actions/workflows/ci.yml/badge.svg)](https://github.com/ms-alan/hermes-go/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/Go-1.24%2B-blue)](https://github.com/nousresearch/hermes-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

基于 Go 1.24 的 AI Agent 框架，Python 版 [hermes-agent](https://github.com/NousResearch/hermes-agent) 的 Go 重写版本。

> 作者 [@ms-alan](https://github.com/ms-alan)

## 特性

- **会话持久化** — SQLite + FTS5，向量搜索支持
- **上下文窗口管理** — 动态上下文压缩，防 token 溢出
- **多平台网关** — QQBot WebSocket 接入，即插即用 PlatformAdapter
- **Skill 插件系统** — SKILL.md 声明式格式，Shell/Python/Go 执行
- **MCP 协议支持** — 客户端 + 服务端，stdio / HTTP 传输
- **配置热加载** — YAML/JSON 配置 + 环境变量覆盖（`HERMES_*` 前缀）
- **内置工具** — file_read / file_write / terminal / web_search

## 快速开始

### 编译

```bash
git clone https://github.com/ms-alan/hermes-go.git
cd hermes-go
go build -o hermes ./cmd/hermes
go build -o hermes-gateway ./cmd/gateway
```

### 配置文件

```bash
cp config.example.yaml ~/.hermes/config.yaml
# 编辑 ~/.hermes/config.yaml，填入你的 API key
```

### CLI 交互模式

```bash
OPENAI_API_KEY=sk-... ./hermes --model=gpt-4o
# 或使用 MiniMax
MINIMAX_API_KEY=... ./hermes --model=MiniMax-Text-01 --base-url=https://api.minimaxi.com/anthropic
```

### QQBot 网关

```bash
QQ_APP_ID=xxx QQ_CLIENT_SECRET=yyy ./hermes-gateway --platforms=qq
```

### HTTP API（可选）

```bash
./hermes-gateway --gateway=:8080
# POST /v1/chat  {"message": "hello"}
# GET  /health
```

## 项目结构

```
hermes-go/
├── cmd/
│   ├── hermes/          # CLI REPL 入口
│   └── gateway/         # QQBot 网关 + HTTP API 入口
├── pkg/
│   ├── agent/           # AIAgent + SessionAgent（会话+上下文包装）
│   ├── model/           # LLM 接口 + OpenAI / MiniMax Provider
│   ├── tools/           # 工具注册表 + 内置工具实现
│   ├── session/         # SQLite FTS5 持久化
│   ├── context/         # 上下文窗口 + 压缩 + 缓存
│   ├── gateway/         # PlatformAdapter 接口
│   │   └── qqbot/       # QQBot WebSocket 适配器
│   ├── skill/           # Skill 插件系统
│   ├── mcp/             # MCP 客户端 + 服务端
│   └── config/          # YAML/JSON 配置加载 + env 覆盖
└── config.example.yaml  # 完整配置示例
```

## 配置说明

配置文件位于 `~/.hermes/config.yaml`，环境变量 `HERMES_*` 可覆盖任意字段：

| 环境变量 | 对应配置 |
|----------|----------|
| `HERMES_MODEL_MODEL_NAME` | `model.model_name` |
| `HERMES_MODEL_API_KEY` | `model.api_key` |
| `HERMES_MODEL_API_BASE` | `model.api_base` |
| `HERMES_AGENT_MAX_RETRIES` | `agent.max_retries` |

完整示例见 [config.example.yaml](config.example.yaml)。

## 内置工具

REPL 和网关均已注册以下内置工具：

| 工具 | 说明 |
|------|------|
| `file_read` | 读取文件（支持 offset/limit） |
| `file_write` | 写入文件（自动创建父目录） |
| `terminal` | 执行 shell 命令（超时保护） |
| `web_search` | 网页搜索（需配置 Tavily/Exa 等） |

## 与 Python 版对比

| 功能 | Python 版 | Go 版 |
|------|-----------|-------|
| 核心 Agent | ✅ | ✅ |
| 会话持久化 | ✅ | ✅ |
| 上下文压缩 | ✅ | ✅ |
| QQBot 接入 | ✅ | ✅ |
| Skill 插件 | ✅ | ✅ |
| MCP 客户端 | ✅ | ✅ |
| MCP 服务端 | ✅ | ✅ |
| 配置管理 | ✅ | ✅ |
| Go 二进制发布 | - | ✅ |
| HTTP API | - | ✅ |

## 技术栈

- **语言**: Go 1.24+
- **数据库**: modernc.org/sqlite (CGO-free)
- **WebSocket**: nhooyr.io/websocket
- **YAML**: gopkg.in/yaml.v3
- **LLM**: OpenAI 兼容 API + MiniMax
