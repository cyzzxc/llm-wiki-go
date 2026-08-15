# MCP 工具参考

实现：`internal/mcpserver`，基于官方 [go-sdk](https://github.com/modelcontextprotocol/go-sdk) v1.7。服务器名 `llm-wiki`，版本 `0.1.0`，能力：tools + resources（list-changed）。

**约定**（与 Rust 原版一致）：

- 所有结果为**单个 text 内容块**：JSON 工具返回 pretty JSON 文本，markdown/渲染类返回原文
- 错误：`isError: true` + 文本 `error: <消息>`；缺参 `missing required parameter: <key>`
- `wiki` 参数省略时用配置的 default wiki
- spaces 增删/设默认后向订阅会话广播 `resources/list_changed`；`wiki_ingest`（非 dry-run）对每个触及页发 `resources/updated`
- resources：每个已索引页面一个 `wiki://<wiki>/<slug>`（MIME `text/markdown`），按索引同步注册

## 空间管理

### wiki_spaces_create
`{path*, name*, description?, force?, set_default?, wiki_root?}` → JSON `{path, name, created, registered, committed}`
建目录结构（inbox/raw/schemas/wiki + .gitkeep + 默认 schema + README + wiki.toml）、git init、首提交、注册、可设默认、热挂载。

### wiki_spaces_register
`{path*, name*, description?, wiki_root?}` → JSON `{path, name, registered}`
不创建文件；读已有 wiki.toml 的 wiki_root，与显式覆盖冲突时报错；校验 wiki_root 合法（相对、非保留字、在 repo 内）。

### wiki_spaces_list
`{name?}` → JSON 数组 `[{name, path, description?, remote?}]`

### wiki_spaces_remove
`{name*, delete?}` → 文本 `Removed wiki "…"`；默认 wiki 拒删（先换默认）。

### wiki_spaces_set_default
`{name*}` → 文本 `Default wiki set to "…"`

## 内容

### wiki_content_read
`{uri*, no_frontmatter?, list_assets?, backlinks?, wiki?}`
- 页面 → 原文 markdown（no_frontmatter 剥离；superseded_by 存在时尾注 `> **Superseded** by …`）
- `list_assets` → bundle 资产 URI 列表（每行一个）
- 资产 → UTF-8 文本或错误 `asset is binary — access it directly from the filesystem`
- `backlinks` → JSON `{content, backlinks: [{slug,title}]}`

### wiki_content_write
`{uri*, content*, wiki?}` → 文本 `Wrote <N> bytes to <path>`。已存在页原位覆写；新页按 flat 布局落 `<slug>.md`。

### wiki_content_new
`{uri*, section?, bundle?, name?, type?, wiki?}` → JSON `{uri, slug, path, wiki_root, bundle}`
脚手架 frontmatter（title/status=draft/last_updated/type/confidence=0.5）+ 正文模板（repo schemas/<type>.md → 内嵌默认）；自动补建父 section。

### wiki_content_commit
`{slugs?, message?, wiki?}` → 提交 hash 文本（无变更空串）。slugs 逗号分隔；省略=全部（`commit: all`）；bundle 页扩展为目录内全部文件。

## 检索

### wiki_search
`{query*, type?, no_excerpt?, include_sections?, top_k?, wiki?, cross_wiki?, format?}`
- 默认 pretty JSON `SearchResult {results: [{slug, uri, title, score, confidence, excerpt?, summary?}], facets: {type, status, tags}}`
- `format: "llms"` → 纯 markdown 行列表（强制无摘要）
- `cross_wiki` 合并全部已挂载库重排
- 中文分词见 docs/tokenizer.md；打分语义见 docs/search.md

### wiki_list
`{type?, status?, page?, page_size?, wiki?, format?}` → JSON `PageList {pages, total, page, page_size, facets}`（llms 格式分组渲染）

### wiki_suggest
`{slug*, limit?, wiki?}` → JSON `[{slug, uri, title, type, score, reason, field}]`
四策略：标签重叠 / 图 2-hop（0.5）/ BM25 相似（归一×0.7）/ 同社区 peers（0.4）；`field` 建议落点（registry 边字段或 `[[wikilink]]`）。

## 索引与摄取

### wiki_ingest
`{path*, dry_run?, redact?, wiki?}` → JSON IngestReport（见 docs/ingestion.md）

### wiki_index_rebuild
`{wiki?}` → JSON `{wiki, pages_indexed, skipped, duration_ms}`

### wiki_index_status
`{wiki?}` → JSON `{built|null, pages, sections, stale, openable, queryable}`

## 图与统计

### wiki_graph
`{format?, root?, depth?, type?, relation?, output?, cross_wiki?, wiki?}` → 渲染文本（mermaid/dot/llms）；`output` 落盘（.md 包裹）。

### wiki_stats
`{wiki?}` → JSON `{wiki, pages, sections, types, status, orphans, avg_connections, graph_density, staleness{fresh,stale_7d,stale_30d}, index{stale,built}, communities|null, diameter|null, radius|null, center, structural_note|null}`

### wiki_lint
`{rules?, severity?, wiki?}` → JSON `{wiki, total, errors, warnings, findings: [{slug, rule, severity, message, path}]}`
规则：`orphan`(w) `broken-link`(e) `broken-cross-wiki-link`(w) `missing-fields`(e) `stale`(w) `unknown-type`(e) `articulation-point`(w) `bridge`(w) `periphery`(w)。

### wiki_history
`{slug*, limit?, follow?, wiki?}` → JSON `{slug, entries: [{hash, date, message, author}]}`

### wiki_export
`{wiki*, path?, format?, status?}` → JSON `{pages, bytes, path}`（llms-txt/llms-full/json，见 CLI 文档同格式）

## 其它

### wiki_resolve
`{uri*, wiki?}` → JSON `{slug, wiki, wiki_root, path, exists, bundle}`（写盘前的路径探测）

### wiki_schema
`{action*, type?, template?, schema_path?, delete?, delete_pages?, dry_run?, wiki?}`
`list` → `[{name, description, schema_path}]`；`show`（`template: true` → frontmatter 模板）；`add` / `remove`（见 docs/schemas.md）；`validate` → `ok` 或问题行。

### wiki_config
`{action*, key?, value?, global?, wiki?}` — get/set/list，见 docs/config.md

### wiki_info
`{}` → JSON `{version, config_path, spaces, default_wiki, index_status: "ok"|"degraded"}`

## 传输

| 方式 | 启动 | 说明 |
|---|---|---|
| stdio | `llm-wiki serve` | 默认（无 --acp/--http 时） |
| HTTP | `llm-wiki serve --http[:PORT]` | Streamable HTTP @ `/mcp`；Host 白名单（serve.http_allowed_hosts）；绑定失败指数退避重试（max_restarts，封顶 30s） |

ACP 与 stdio 互斥（ACP 独占），HTTP 可与 ACP 并存。
