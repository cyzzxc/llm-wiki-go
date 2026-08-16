# AGENTS.md — Agent 工作上下文

面向在此仓库工作的 AI agent 与贡献者。快速定向，不是规范——深潜看 `docs/`。
上游项目：[geronimo-iia/llm-wiki](https://github.com/geronimo-iia/llm-wiki)（Rust）。
本项目是其 Go 改写，功能对齐，中文分词为一等公民。

## 代码地图

| 路径 | 职责 |
|---|---|
| `cmd/llm-wiki/main.go` | CLI 入口：全局 flag 解析 + 子命令分发 |
| `cmd/llm-wiki/commands.go` | 全部 CLI 子命令（文本/JSON/llms 输出格式在此层） |
| `cmd/llm-wiki/serve.go` | serve/watch 命令：传输编排、HTTP/ACP/watch/心跳 |
| `internal/wiki/` | 引擎域（一个包，按文件分域，见下） |
| `internal/wiki/slug.go` | slug 校验/解析、`wiki://` URI、read-target 两步解析 |
| `internal/wiki/frontmatter.go` | YAML frontmatter 解析（宽松/严格）、脚手架、confidence |
| `internal/wiki/page.go` | 页面 CRUD、bundle/asset、strip frontmatter |
| `internal/wiki/links.go` | `[[wikilink]]` + CommonMark 链接提取与相对路径归一 |
| `internal/wiki/config.go` | 全局 `config.toml` + 每 wiki `wiki.toml`，dot-key get/set |
| `internal/wiki/gitx.go` | git CLI 封装：commit/diff/porcelain/log（无 libgit2） |
| `internal/wiki/schema.go` | 类型注册表 + 索引字段分类（x-wiki-types 等扩展） |
| `internal/wiki/index.go` | BM25 索引、`IndexManager`（rebuild/update/状态/staleness） |
| `internal/wiki/search.go` | 查询/分面/摘要高亮/llms 渲染/backlinks |
| `internal/wiki/graph.go` | 概念图、Louvain、Tarjan 割点/桥、渲染器、快照缓存 |
| `internal/wiki/ingest.go` | ingest 校验流水线 + redact 脱敏 |
| `internal/wiki/spaces.go` | 多 wiki 空间创建/注册/删除 |
| `internal/wiki/engine.go` | `WikiEngine`/`SpaceContext`，mount 与智能重建 |
| `internal/wiki/ops_*.go` | ops 层：CLI 与 MCP 共用的操作入口（单一事实源） |
| `internal/wiki/logging.go` | slog 轮转文件日志（daily/never + prune） |
| `internal/tokenizer/` | 中文/拉丁分词（gse 词典 + 脚本路由） |
| `internal/embed/` | OpenAI 兼容嵌入客户端（批量/重试/归一化）——语义搜索，默认关 |
| `internal/assets/` | 内嵌资产：zh 词典 + 默认 schema/模板 |
| `internal/mcpserver/` | MCP：24 个 wiki_* 工具 + resources + stdio/HTTP 传输 |
| `internal/acpserver/` | ACP v1 agent：JSON-RPC over stdio + 5 个工作流 |
| `internal/web/` | 只读 Web UI（`serve --web`）：服务端渲染 + goldmark + wikilink 预处理 |
| `internal/watch/` | fsnotify + debounce 自动 ingest |

## 关键类型

- `WikiEngine` — `mu sync.RWMutex` + `EngineState`；持有全部已挂载空间
- `SpaceContext` — 一个 wiki 的全部运行时状态（roots、registry、index、graph cache、resolved config）
- `IndexManager` — 索引生命周期；`LastCommit()` 读**内存** `m.state`（由 `Open()` 从 state.toml 加载）
- `SearchIndex` — 不可变内存索引；`Docs` 可导出（gob），`bySlug/df/totalLen` 是派生结构，**gob 解码后必须 `rebuildStats()`**
- `GraphCache` — 按 `IndexManager.Generation()` 代数缓存；可选 gob+gzip 快照

## 不变量（改代码前必读）

1. **ops 层是单一事实源**。CLI（`commands.go`）与 MCP（`mcpserver`）都只调 `Ops*` 函数。副作用（索引刷新、图缓存重建、edge-target 校验）属于 ops 层，不许在调用方补。
2. **图/社区缓存与快照键用 `LastCommit()`（state.toml 里的 git HEAD），不用 `Generation()`**。generation 每次进程重启归零，跨进程不稳定（曾导致吃过期快照）。
3. **gob 只编码导出字段**。任何改动 `SearchIndex`/`WikiGraph` 字段后，确认解码路径调用了 `rebuildStats()` / 重建 `adjOut/adjIn`；否则跨进程检索静默为空（踩过）。
4. **`persist()` 必须同时写 state.toml 和 index.gob**。只写 gob 会让增量更新锚点（last commit）丢失（踩过）。
5. **git porcelain 与 diff 必须 `-c core.quotePath=false`**，否则中文路径被 C 转义，changed-paths 匹配失效（踩过）；重命名必须双向记录（新路径 + 旧路径删除）。
6. **stdio 独占**：ACP 启用时独占 stdin/stdout；HTTP/ACP/Web 可任意共存；MCP stdio 仅在 HTTP、ACP、Web 三者都未启用时运行（`--web` 单开也抑制 stdio——后台 stdin EOF 会停掉整个 serve，踩过）。同抢 stdio 会静默吞消息。
7. **索引与查询必须用同一个 tokenizer**（名称记录在索引里，`Open()` 检测变更即强制重建）。查询侧换分词器 = 匹配断裂。
8. **confidence 缺席是语义**：`Confidence *float64` 为 nil 时乘子取 1.0，绝不伪造 0.5。status 缺席取 `unknown` 乘子（默认 0.9）。
9. **slug 永远 POSIX 风格**（`/` 分隔）；跨平台路径在 `SlugFromPath`/git 层做转换。

## 嵌入（语义搜索）规约

- `[embedding]` 是**可选外联**：未启用时引擎完全离线；任何改动不得让默认路径产生网络调用。
- 同模型硬约束：state 记 `embedding_model`，配置变更 → stale → 全量重嵌；查询与索引必须同网关同模型。
- 嵌入 pass 失败**降级不阻塞**（无向量 + 警告）；增量更新只嵌无向量的文档。
- 网络相关测试只用 httptest 假网关（`internal/embed/embed_test.go`、`internal/wiki/semantic_test.go` 的确定性标记向量）；真实网关只进手工冒烟。

## 中文分词规约

- 模式：`auto`（默认，懒加载）/ `gse|zh|cjk`（急切）/ `simple|en_stem`（无词典，Han 退化为 unigram）。
- 词典是**编译期内嵌**（`internal/assets/dict/zh_s.txt`，jieba 词频，经 gse 格式）。换词典 = 替换该文件 + 重建。
- 加载词典约 400ms / ~100MB RSS——一次性成本；不要在任何热路径重复构造 tokenizer。
- 详细设计：`docs/tokenizer.md`。

## 测试模式

- `go test ./...`；集成测试在 `internal/wiki/wiki_test.go`（`buildFixture` 造临时 home + 中文页面 + git 提交）。
- **跨进程回归**：任何涉及持久化的改动，用 `BuildEngine` 两次（同 config）模拟两个进程生命周期（见 `TestSearchAcrossProcessReload`）。
- 冒烟（人工）：MCP stdio（initialize/tools/list/wiki_search）、HTTP（curl `/mcp`）、ACP（python 驱动 session/new + prompt）。
- 性能基准：`go test ./internal/wiki -bench BenchmarkSearch -run=XXX`。
- 测试运行时注意 `$HOME`：fixture 依赖 `os.UserHomeDir`，shell 里先 `export HOME=...` 指向临时目录。

## 构建与质量门

```console
go build -o bin/llm-wiki ./cmd/llm-wiki   # 20MB（-ldflags="-s -w" 后 15MB）
go test ./...
go vet ./...
gofmt -l .                                  # 必须为空
```

## 写文档/代码的约定

- 注释解释**约束**，不解释显而易见的操作。
- 与 Rust 原版的行为差异必须记录在 `docs/parity.md`，不许静默偏离。
- 用户数据只有 Markdown + git；`~/.llm-wiki/` 下全是可再生缓存——任何新持久化物都得能安全删除。
