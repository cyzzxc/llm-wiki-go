# llm-wiki-go

[geronimo-iia/llm-wiki](https://github.com/geronimo-iia/llm-wiki) 的 Go 改写：**git-backed 无头 wiki 引擎**——类型化 Markdown 页面、BM25 全文检索、概念图谱，通过 MCP / ACP / CLI 服务 agent。

本移植的核心增强：**中文分词是一等公民**。Rust 原版的 tantivy `en_stem` 分词器对中文无效（无空格即无词界）；Go 版自带嵌入词典的中文分词器，中英混写内容开箱即检。

## 文档

- **[AGENTS.md](AGENTS.md)** — agent/贡献者工作上下文（代码地图、不变量、测试模式）
- [docs/overview.md](docs/overview.md) — 系统总览与写入生命周期
- [docs/architecture.md](docs/architecture.md) — 分层、并发模型、持久化物
- [docs/tokenizer.md](docs/tokenizer.md) — 中文分词设计（模式/词典/成本模型）
- [docs/search.md](docs/search.md) — BM25 检索语义与偏差
- [docs/indexing.md](docs/indexing.md) — 索引生命周期（rebuild/update/staleness/state）
- [docs/graph.md](docs/graph.md) — 概念图谱、Louvain、结构算法、渲染
- [docs/schemas.md](docs/schemas.md) — 页面类型系统（x-wiki-* 扩展协议）
- [docs/ingestion.md](docs/ingestion.md) — ingest 流水线与 redact 脱敏
- [docs/config.md](docs/config.md) — 配置参考（全键）
- [docs/mcp.md](docs/mcp.md) — MCP 24 工具参考
- [docs/acp.md](docs/acp.md) — ACP 协议与工作流
- [docs/cli.md](docs/cli.md) — CLI 参考
- [docs/invariants.md](docs/invariants.md) — 行为不变量
- [docs/parity.md](docs/parity.md) — 与 Rust 原版对齐清单与差异
- [docs/testing.md](docs/testing.md) — 测试与冒烟手册

```console
$ llm-wiki search "注意力机制"
slug:  concepts/attention
uri:   wiki://mywiki/concepts/attention
title: 注意力机制
score: 6.76
excerpt: # <b>注意</b>力<b>机制</b> …
```

## 快速开始

```console
go build -o llm-wiki ./cmd/llm-wiki

llm-wiki spaces create ~/code/mywiki --name mywiki --set-default
# 写入 Markdown 页面（带 YAML frontmatter）…
llm-wiki ingest .
llm-wiki search "混合专家模型"
llm-wiki graph --format mermaid
llm-wiki serve            # MCP stdio server（接入 Claude / Zed 等）
```

数据归你：页面是普通 Markdown + git 仓库；索引与快照都是可再生的派生缓存（`~/.llm-wiki/`），删掉即重建。

## 中文分词

| 模式 | 行为 | 适用 |
|---|---|---|
| `auto`（默认） | 汉文段走 gse 词典分词（懒加载），拉丁段走词分词 | 中英混写（绝大多数场景） |
| `gse` / `zh` / `cjk` | 同 auto，进程启动即加载词典 | 纯中文库、希望尽早暴露词典问题 |
| `simple` / `en_stem` | 汉字退化为单字 unigram，拉丁按词 | 纯英文库、内存受限 |

```toml
# ~/.llm-wiki/config.toml
[index]
tokenizer = "auto"   # auto | gse | simple（en_stem 为兼容别名）
```

语义搜索（可选）：配置 OpenAI 兼容嵌入网关后，`wiki_search` 获得 `semantic`（纯向量召回）与 `hybrid`（混合排序）模式——可召回同义改写、跨语言等无词面重叠的内容；默认关闭，未启用时引擎完全离线。详见 [docs/search.md](docs/search.md)。

```toml
[embedding]
enabled = true
base_url = "http://192.168.6.2:48080/v1"   # 例：AxonHub 网关
model = "qwen3-embedding-8b"               # 索引与查询必须同模型
# api_key 建议走环境变量 LLM_WIKI_EMBEDDING_API_KEY
```

实现要点：

- **词典内嵌**：jieba 词频词典（~35 万词，4.9MB，经 [go-ego/gse](https://github.com/go-ego/gse) Apache-2.0 格式）编译进二进制，单文件分发，无外部依赖。
- **索引/查询同源**：索引用 `CutSearch` 搜索模式（全词 + 子词，CJK 检索的标准召回策略），查询用同一分词器——「混合专家模型」的查询同时命中全词与「专家」「模型」子词。
- **脚本路由**：文本按 Unicode 脚本切run，汉文段入词典分词，拉丁段按字母数字词切分，互不污染（`BM25检索` → `bm25` + `检索`）。
- **懒加载**：纯英文 wiki 永不加载词典（省 ~100MB RSS 与 ~400ms 启动）。

## 架构（与 Rust 原版模块对应）

| Rust (src/) | Go (internal/) | 说明 |
|---|---|---|
| slug.rs | wiki/slug.go | slug 校验/解析、wiki:// URI |
| frontmatter.rs | wiki/frontmatter.go | YAML frontmatter 解析/脚手架 |
| markdown.rs | wiki/page.go | 页面 CRUD、bundle/asset |
| links.rs | wiki/links.go | wikilink + CommonMark 链接提取 |
| config.rs | wiki/config.go | 全局/每 wiki TOML 配置 |
| git.rs | wiki/gitx.go | **git CLI**（原版 libgit2 + log 已 shell out） |
| type_registry.rs + space_builder.rs + index_schema.rs | wiki/schema.go | x-wiki-types / x-graph-edges / x-index-aliases，JSON Schema 校验，字段分类 |
| index_manager.rs + tantivy | wiki/index.go + search.go | **手写 BM25**（gob 持久化 + state.toml） |
| graph.rs + petgraph | wiki/graph.go | 概念图、Louvain、Tarjan 割点/桥、BFS 直径 |
| ingest.rs + ops/redact.rs | wiki/ingest.go | 校验流水线 + 6 内置脱敏模式 |
| spaces.rs | wiki/spaces.go | 多 wiki 空间注册 |
| engine.rs | wiki/engine.go | 挂载、增量/部分重建、图缓存 |
| mcp/ | mcpserver/ | **官方 go-sdk**：24 个 wiki_* 工具 + resources；stdio + Streamable HTTP |
| acp/ | acpserver/ | **手写 ACP v1 JSON-RPC**：research/lint/graph/ingest/use 工作流 |
| watch.rs | watch/ | fsnotify + debounce 自动 ingest |
| ops/*.rs | wiki/ops_*.go | CLI/MCP 共用的操作层 |

## MCP 工具（24 个，与原版一一对应）

`wiki_spaces_create / register / list / remove / set_default`、`wiki_config`、
`wiki_content_read / write / new / commit`、`wiki_search`、`wiki_list`、`wiki_ingest`、
`wiki_index_rebuild / status`、`wiki_graph`、`wiki_export`、`wiki_history`、`wiki_stats`、
`wiki_suggest`、`wiki_lint`、`wiki_resolve`、`wiki_schema`、`wiki_info`

传输：`llm-wiki serve`（stdio）、`--http[:PORT]`（Streamable HTTP @ `/mcp`，Host 白名单 + 绑定重试）、`--acp`（ACP 独占 stdio，接入 Zed）、`--watch`（文件保存自动 ingest）。

## 预算实测（M1, go1.26）

| 指标 | 实测 | 预算 |
|---|---|---|
| 二进制体积 | 20MB（`-s -w` 后 15MB，含 4.9MB 词典） | ≤20MB |
| 1000 页中文库全量重建 | 188ms（另含一次性词典加载 ~400ms） | <1s |
| 内存检索延迟 | 0.38ms @1k 页；5.8ms @10k 页 | p95 <50ms |
| CLI 单发查询 | ~435ms（进程启动 + 词典加载；常驻服务只付一次） | — |

## 与 Rust 原版的差异（对齐前提下的实现替换）

- **tantivy → 手写 BM25**：字段加权（title×3 / summary×2 / body×1）合并计权，而非 tantivy 的 per-field BM25；摘要高亮为自实现窗口 + `<b>` 包裹。k1=1.2 / b=0.75 与 tantivy 默认一致。
- **libgit2 → git CLI**：commit/diff/log 全部 shell out（原版 `page_history` 本就如此）。要求环境有 git。
- **图快照**：gob + gzip（配置名 `bincode` / `bincode+lz4` / `bincode+zstd` 兼容保留，压缩一律 gzip）——纯内部缓存格式，无互操作需求。
- **索引持久化**：`index.gob` + `state.toml`（字段与原版 state.toml 对齐）。
- **日志轮转**：`daily | never`（原版另有 `hourly`，按 `daily` 处理）。
- **MCP resources**：go-sdk 静态注册模型，spaces 变更时增量同步并广播 list-changed；resource-updated 通知发给订阅会话。
- **watch**：fsnotify；运行中新建的子目录需重建 watcher 才会被监控（已知限制）。
- **`[[slug|alias]]` 管道别名**：与原版一致，不支持（整个串按 slug 处理）。

## 测试

```console
go test ./...        # 单元 + 中文语料集成（检索/图谱/lint/脱敏/增量更新/跨进程重载）
```

冒烟已人工验证：MCP stdio（initialize / tools/list 24 工具 / wiki_search 中文）、MCP Streamable HTTP、ACP research 工作流（流式 tool_call / end_turn）。

## 许可与致谢

- 上游项目 [geronimo-iia/llm-wiki](https://github.com/geronimo-iia/llm-wiki)（MIT OR Apache-2.0）——类型 schema 与页面模板自其复制。
- 中文词典：结巴分词词典，经 [go-ego/gse](https://github.com/go-ego/gse)（Apache-2.0）。
