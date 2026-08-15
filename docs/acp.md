# ACP（Agent Client Protocol）实现

`internal/acpserver` 手写实现 ACP v1 的 **agent 侧**——面向 Zed 等编辑器内嵌使用。JSON-RPC 2.0 over stdio（换行分隔）。

## 启动

`llm-wiki serve --acp`（或配置 `[serve] acp = true`）。**ACP 独占 stdio**——MCP stdio 不再启动；HTTP 可并存。启动日志 `transports=[acp]`。

## 协议面

### 请求（client → agent）

| 方法 | 行为 |
|---|---|
| `initialize` | 返回 `protocolVersion: 1`、agentCapabilities（loadSession / promptCapabilities / sessionCapabilities.list）、agentInfo `{name: "llm-wiki", version}` |
| `session/new` | 会话上限 `serve.acp_max_sessions`（默认 20，超限 -32602）；`params.meta.wiki` 可绑库；id `session-<UnixMilli>` |
| `session/load` | 会话存在校验 |
| `session/list` | `[{sessionId, cwd(默认库 repo 根), title}]`；运行中前缀 `[active]` |
| `session/prompt` | 分发工作流（见下），返回 `{stopReason: "end_turn"}` |
| `session/cancel` | 置会话取消标志 + 清 active run（协作式：工作流步骤间轮询） |
| 其它带 id 请求 | `-32601 not supported` |

### 通知（agent → client，均走 `session/update`）

| sessionUpdate | 载荷 | 用途 |
|---|---|---|
| `agent_message_chunk` | `{content: {type: "text", text}}` | 流式文本 |
| `tool_call` | `{toolCallId, title, kind, status: "in_progress"}` | 步骤开始（kind: search/read/other） |
| `tool_call_update` | `{toolCallId, status: completed|failed, content: [text块]}` | 步骤结果 |

`toolCallId = <workflow>-<step>-<UnixMilli>`。

## 工作流分发

`dispatchWorkflow`：`llm-wiki:<workflow> <text>` 前缀语法；裸 prompt 默认 research。

| 工作流 | 步骤 | 输出 |
|---|---|---|
| `research` | 搜索 top5（tool_call）→ 读 top1 → 报告 | `Searching for: …` / `No results found for "…" in wiki "…"` / `Based on N pages in "wiki": - uri (score: x.xx)` |
| `lint` | OpsLint（可传规则表） | 逐条 `[severity] slug: message`，取消即停 |
| `graph` | OpsGraphBuild（llms 格式，query 作 root） | 图渲染全文 + `Graph: N nodes, M edges` |
| `ingest` | OpsIngest（query 空=默认库 repo 根） | `N pages validated, M unchanged, K warnings — commit xxxxxxxx` |
| `use <slug>` | 读整页并流式输出 | 用法提示或页面全文 |
| `help` / 未知 | — | 工作流清单 |

## watcher 推送

`serve --acp --watch` 时，文件变更 ingest 完成后向**同 wiki 的空闲会话**推送 `agent_message_chunk`（`wiki updated and re-ingested`）。

## 与 Rust 原版差异

- Rust 用 `agent-client-protocol` crate；Go 手写 JSON-RPC 子集（initialize/new/load/list/prompt/cancel + session/update 通知），行为对齐，未实现的 client→agent 方法一律 -32601。
- 会话不持久化（内存 map）——与原版一致（list 只见本进程会话）。

## 冒烟（python 驱动）

```python
send {"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}
send {"jsonrpc":"2.0","id":2,"method":"session/new","params":{"mcpServers":[]}}
sid = recv()["result"]["sessionId"]
send {"jsonrpc":"2.0","id":3,"method":"session/prompt",
      "params":{"sessionId":sid,"prompt":[{"type":"text","text":"llm-wiki:research 混合专家模型"}]}}
# 流式收 session/update 直到 id=3 响应 {"stopReason":"end_turn"}
```
