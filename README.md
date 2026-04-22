# Hermes Agent (Go Version)

基于 Go 1.24 的 AI Agent 框架，Python 版 [hermes-agent](https://github.com/NousResearch/hermes-agent) 的 Go 重写版本。

> 作者 [@ms-alan](https://github.com/ms-alan) · 武汉 · AI 虚拟主播场景

## 特性

- **会话持久化** — SQLite + FTS5，向量搜索支持
- **上下文窗口管理** — 动态上下文压缩，防 token 溢出
- **多平台网关** — QQBot WebSocket 接入，即插即用 PlatformAdapter
- **Skill 插件系统** — SKILL.md 声明式格式，Shell/Python/Go 执行
- **MCP 协议支持** — 客户端 + 服务端，stdio / HTTP 传输
- **配置热加载** — YAML/JSON 配置 + 环境变量覆盖
- **工具注册表** — file / terminal / web_search / send_message 等内置工具

## 快速开始

```bash
# 编译
cd hermes-go
go build -o hermes ./cmd/hermes
go build -o hermes-gateway ./cmd/gateway

# CLI 交互模式
MINIMAX_CN_API_KEY=your_key ./hermes

# 启动 QQBot 网关
QQ_APP_ID=xxx QQ_CLIENT_SECRET=xxx ./hermes-gateway --platforms=qq
```

## 项目结构

```
hermes-go/
├── pkg/
│   ├── agent/      # AIAgent + SessionAgent (会话+上下文包装)
│   ├── model/      # LLM 接口 + OpenAI / MiniMax Provider
│   ├── tools/      # 工具注册表
│   ├── session/    # SQLite FTS5 持久化
│   ├── context/    # 上下文窗口 + 压缩 + 缓存
│   ├── gateway/    # PlatformAdapter 接口
│   │   └── qqbot/  # QQBot WebSocket 适配器
│   ├── skill/      # Skill 插件系统
│   ├── mcp/        # MCP 客户端 + 服务端
│   └── config/     # YAML/JSON 配置加载
├── cmd/
│   ├── hermes/     # CLI REPL 入口
│   └── gateway/    # QQBot 网关入口
├── hermes          # 编译好的 CLI 二进制
└── hermes-gateway  # 编译好的网关二进制
```

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

## 技术栈

- **语言**: Go 1.24+
- **数据库**: modernc.org/sqlite (CGO-free)
- **WebSocket**: nhooyr.io/websocket
- **YAML**: gopkg.in/yaml.v3
- **LLM**: OpenAI 兼容 API + MiniMax
