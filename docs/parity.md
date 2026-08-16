# 与 Rust 原版对齐清单与差异

上游：[geronimo-iia/llm-wiki](https://github.com/geronimo-iia/llm-wiki)（MIT OR Apache-2.0）。Go 版功能对齐；差异全部列于此，无静默偏离。核心增量：中文分词（docs/tokenizer.md）。

## 功能对齐表

| 能力 | Rust | Go | 状态 |
|---|---|---|---|
| MCP 工具 | 24 个（rmcp） | 24 个同名同参（官方 go-sdk） | ✅ 对齐 |
| MCP 传输 | stdio + Streamable HTTP（allowed hosts、绑定重试） | 同 | ✅ 对齐 |
| MCP resources + 通知 | walk 全空间 + updated/list_changed | 索引同步注册 + 同语义通知 | ✅ 对齐（注册机制不同，见下） |
| ACP | agent-client-protocol crate，5 工作流 | 手写 JSON-RPC 子集，同工作流 | ✅ 对齐（协议库差异） |
| CLI | clap 全命令树 | 同命令树 + 同输出格式 | ✅ 对齐 |
| 搜索 | tantivy BM25 + 状态/置信度乘子 + 分面 + 摘要 | 手写 BM25 同参数同乘子 | ✅ 行为对齐（实现偏差见下） |
| 中文检索 | ❌ en_stem 对中文无效 | gse 词典分词一等公民 | ➕ 增量 |
| 语义检索 | ❌ 无 | [embedding] 可选层：semantic/hybrid 模式，OpenAI 兼容网关 | ➕ 增量 |
| 索引生命周期 | state.toml + 增量 + 部分重建 + staleness 四态 | 同 | ✅ 对齐 |
| 图 | petgraph + Louvain p1 + 快照 + 跨库合并 | 内建图 + 同算法 | ✅ 对齐 |
| 结构算法 | 割点/桥/直径/半径/中心/边缘 | Tarjan/BFS 同语义 | ✅ 对齐 |
| Lint | 9 规则 | 9 规则同严重级 | ✅ 对齐 |
| Suggest | 4 策略 + field 建议 | 同 | ✅ 对齐 |
| Export | llms-txt/llms-full/json | 同模板 | ✅ 对齐 |
| Schema 系统 | x-wiki-types/graph-edges/index-aliases/keyword + 校验 + add/remove | 同 | ✅ 对齐 |
| Ingest + redact | 6 内置模式 + 自定义 + dry-run | 同 | ✅ 对齐 |
| Spaces | create/register/list/remove/set-default + 热挂载 | 同 | ✅ 对齐 |
| 配置 | 全段 + dot-key get/set + 每 wiki 覆盖 | 同键集 | ✅ 对齐 |
| watch | notify + debounce | fsnotify + debounce | ✅ 对齐（新子目录限制，见下） |
| 日志 | 轮转 hourly/daily/never + tail/list/clear | daily/never（hourly 视同 daily） | ⚠️ 部分对齐 |
| 类型化 schema 资产 | schemas/ 内嵌 | 同文件复制内嵌 | ✅ 对齐 |

## 实现替换（行为影响已评估）

| # | Rust | Go | 影响 |
|---|---|---|---|
| 1 | tantivy per-field BM25 | 单一加权词袋（title×3/summary×2/body×1，k1=1.2 b=0.75 同） | 得分数值不同、排序语义近似；标题命中仍显著领先 |
| 2 | tantivy QueryParser（field:term 等） | 整串分词 OR | 无字段查询语法；type 过滤走参数 |
| 3 | snippet generator 按 AST 高亮 | 首命中词窗口 + token 子串 `<b>` 包裹 | 拉丁可能部分词匹配高亮 |
| 4 | libgit2（log 已 shell out） | git CLI 全量 | 需环境有 git；无行为差异 |
| 5 | bincode(+lz4/zstd) 图快照 | gob+gzip（配置名兼容保留） | 内部缓存格式，无互操作需求 |
| 6 | tantivy 目录索引 | index.gob + state.toml | 内部格式 |
| 7 | rmcp resources 动态 list | go-sdk 静态 AddResource + spaces 变更时同步 | 客户端可见行为一致 |
| 8 | tracing + rolling file | slog + 自写轮转 | hourly 不支持 |

## Go 版独有增量

- **中文分词**：docs/tokenizer.md。
- **语义搜索**：`[embedding]` 段（默认关）+ `wiki_search.mode` / CLI `--semantic`/`--hybrid`；OpenAI 兼容 `/embeddings` 网关（实测 AxonHub + qwen3-embedding-8b，4096 维）；同模型约束，换模型自动全量重嵌。设计见 docs/search.md。

## 已知限制（Go 版）

- watch 运行中新建的子目录需重建 watcher 才被监控（fsnotify 不递归新目录）。
- `[[slug|alias]]` 管道别名：与原版一致不支持。
- 繁体词典未内嵌（zh_s 简体）——繁体内容检索退化为子词/单字命中，仍可用。
- 拉丁词无词干化（`en_stem` 为兼容别名）。
- MCP 会话间无资源订阅差异语义（SDK 全局广播给订阅者）。

## 有意保留的「怪癖」（agent 依赖的行为）

- 错误字符串精确格式（`missing required parameter: key`、`unknown tool: …`、`wiki "x" is not mounted`…）
- digest 输出模板（`Wrote N bytes to P`、`Ingested: …`、CLI 各表头列宽）
- lint 中 `sections:` 等对齐列（7/8 字符键宽）
- `commit:` 默认信息格式（`commit: all` / `commit: a, b`）

## schema 与许可

`schemas/*.json|md` 复制自上游（MIT OR Apache-2.0）；中文词典为 jieba 词频（经 go-ego/gse，Apache-2.0）。
