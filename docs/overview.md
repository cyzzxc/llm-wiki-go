# 总览

llm-wiki-go 是 [geronimo-iia/llm-wiki](https://github.com/geronimo-iia/llm-wiki)（Rust）的 Go 改写：**git-backed 无头 wiki 引擎**，面向 agent 而非人类浏览——知识随每次写入复利累积（DKR 模式），而非查询时一次性构建（RAG 模式）。

## 核心思想（继承自原版）

- **引擎不含 LLM**。所有智能通过 MCP 工具或 ACP 工作流由外部模型注入；引擎只管文件、git、全文检索和图结构。
- **摄取时整合**。agent 在写页面时（`wiki_content_write` / `wiki_ingest`）就把知识并入已有页面，检索只是回放。
- **类型化页面**。每页是带 YAML frontmatter 的 Markdown（type/status/confidence/tags/sources/concepts…），frontmatter 由 JSON Schema 校验，图边从声明字段自动提取。
- **数据归用户**。页面 = 纯文本 + git 历史；索引、图快照、日志都是可再生的派生缓存。

## 系统形状

```
                    ┌────────────────────────────────────────────┐
                    │                llm-wiki 二进制              │
                    │                                            │
  agent (LLM) ──MCP──▶ mcpserver ──┐                             │
  Zed/IDE     ──ACP ──▶ acpserver ──┤  ops 层（单一事实源）         │
  人类         ──CLI ──▶ commands ──┘      │                      │
  文件保存     ──watch────────────────────┤                      │
                                          ▼                      │
                                    WikiEngine                   │
                              （多 wiki 空间挂载）                  │
                     ┌────────────────────┼──────────────────┐    │
                     ▼                    ▼                  ▼    │
               IndexManager         GraphCache        TypeRegistry│
              （BM25 索引+state）   （图+Louvain）   （JSON Schema）│
                     │                    │                      │
                     ▼                    ▼                      ▼
               tokenizer(中文)        git CLI              schemas/ │
                                                                          │
   用户数据：  <repo>/wiki/**/*.md + git        派生缓存：~/.llm-wiki/ ──┘
```

## 一条写入的生命周期

1. agent 调 `wiki_content_write`（或直接写盘后 `wiki_ingest`）
2. ops 层校验 frontmatter（schema + 类型严格度），可选 redact 脱敏
3. `git commit`（auto_commit 可关）
4. `IndexManager.Update` 按 git diff 增量重索引变更页
5. 下一次 `wiki_search` / `wiki_graph` / `wiki_stats` 立即可见

## 传输面

| 传输 | 启用 | 说明 |
|---|---|---|
| MCP stdio | 默认（无 --acp/--http 时） | 接入 Claude Code、Cursor 等 |
| MCP Streamable HTTP | `serve --http[:PORT]` | `/mcp` 端点，Host 白名单 + 绑定重试 |
| ACP v1 | `serve --acp` | 独占 stdio，接入 Zed；research/lint/graph/ingest/use 工作流 |
| watch | `serve --watch` 或 `watch` 命令 | fsnotify + debounce 自动 ingest |

## 目录

- `architecture.md` — 模块依赖、数据流、并发模型
- `tokenizer.md` — 中文分词设计
- `search.md` — BM25 检索设计
- `indexing.md` — 索引生命周期与持久化
- `graph.md` — 概念图谱与结构算法
- `schemas.md` — 页面类型系统
- `ingestion.md` — ingest 流水线与脱敏
- `config.md` — 配置参考（全键）
- `mcp.md` — MCP 工具参考（24 个）
- `acp.md` — ACP 协议实现
- `cli.md` — CLI 参考
- `invariants.md` — 行为不变量
- `parity.md` — 与 Rust 原版对齐清单与差异
- `testing.md` — 测试与冒烟手册
