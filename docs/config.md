# 配置参考

配置解析顺序：`--config` flag → `$LLM_WIKI_CONFIG` → `~/.llm-wiki/config.toml`。
每 wiki 的 `wiki.toml` 覆盖全局对应段（标 ◈ 的段）；`search.status` 逐键合并。
所有段都有安全默认值——零配置可用。

## 全局 `~/.llm-wiki/config.toml`

```toml
[global]
default_wiki = "mywiki"        # --wiki 缺省目标

[[wikis]]
name = "mywiki"                # wiki:// URI 与 --wiki 用的名字
path = "/abs/path/to/repo"     # 仓库根（wiki/ 的父目录）
description = "可选一行描述"
remote = "可选 git remote"

[defaults]                     # ◈ CLI/工具缺省
search_top_k = 10              # wiki_search 默认结果数
search_excerpt = true          # 是否带 BM25 摘要
search_sections = false        # 结果是否含 section 索引页
page_mode = "flat"             # flat | hierarchical（展示模式）
list_page_size = 20            # wiki_list 每页条数
output_format = "text"         # text | json
facets_top_tags = 10           # tags 分面返回上限（0=全部）

[read]                         # ◈
no_frontmatter = false         # content read 默认剥 frontmatter

[index]
auto_rebuild = false           # 挂载时索引落后自动重建
auto_recovery = true           # 损坏索引自动恢复
memory_budget_mb = 50          # 兼容保留（Go 内存索引无分段预算）
tokenizer = "auto"             # ★ auto | gse|zh|cjk | simple|en_stem（见 docs/tokenizer.md）

[graph]                        # ◈
format = "mermaid"             # mermaid | dot | llms
depth = 3                      # 子图默认跳数
type = []                      # 默认包含的类型（空=全部）
output = ""                    # 默认输出文件（空=stdout）
min_nodes_for_communities = 30 # Louvain 最小节点门槛
community_suggestions_limit = 2
snapshot = true                # 图快照 warm-start
snapshot_keep = 3              # 快照保留份数
snapshot_format = "bincode+lz4" # bincode|bincode+lz4|bincode+zstd|gob|gob+gzip（Go 一律 gob+gzip，压缩名仅区分是否压缩）
structural_algorithms = true   # stats 是否算直径/半径/中心
max_nodes_for_diameter = 2000  # 直径族算法节点上限

[serve]
http = false                   # 默认启用 HTTP 传输
http_port = 8080
http_allowed_hosts = ["localhost", "127.0.0.1", "::1"]   # Host 白名单
acp = false                    # ACP 独占 stdio
max_restarts = 10              # HTTP 绑定重试次数
restart_backoff = 1            # 初始退避秒数（指数翻倍，封顶 30s）
heartbeat_secs = 60            # 心跳日志间隔（0=关）
acp_max_sessions = 20

[validation]                   # ◈
type_strictness = "loose"      # loose | strict（见 docs/schemas.md）

[ingest]                       # ◈
auto_commit = true             # ingest 后自动 git commit

[history]                      # ◈
follow = true                  # git log --follow
default_limit = 10

[suggest]                      # ◈
default_limit = 5
min_score = 0.1                # 建议收纳阈值

[search]                       # ◈（status 表逐键合并）
[search.status]
active = 1.0                   # BM25 得分乘子
draft = 0.8
archived = 0.3
unknown = 0.9                  # 缺席/未知 status 的乘子

[lint]                         # ◈
stale_days = 90                # stale 规则：超过天数
stale_confidence_threshold = 0.4   # 且 confidence 低于此值才报

[logging]
log_path = "~/.llm-wiki/logs"  # 空 = 仅 stderr
log_rotation = "daily"         # daily | never
log_max_files = 7
log_format = "text"            # text | json

[watch]
debounce_ms = 500

[embedding]                   # 语义搜索（Go 版增量；global-only，默认关）
enabled = false               # 关闭 = 引擎完全离线，纯 BM25
base_url = ""                 # OpenAI 兼容网关，如 http://192.168.6.2:48080/v1
api_key = ""                  # 或环境变量 LLM_WIKI_EMBEDDING_API_KEY（优先）；config get/list 输出打码
model = "qwen3-embedding-8b"  # ★ 索引与查询必须同模型；换模型自动触发全量重建
batch_size = 16               # 每次 /embeddings 请求的文本数（批量摊薄 ~1.4s/条延迟）
max_text_chars = 4000         # 每页嵌入文本截断（title+summary+body，rune 计）
timeout_secs = 60
hybrid_weight = 0.5           # hybrid 模式余弦权重（1−w 给归一化 BM25）

[redact]                       # ◈
disable = []
patterns = []                  # [{name, pattern, replacement}] 见 docs/ingestion.md
```

## 每 wiki `<repo>/wiki.toml`

```toml
name = "mywiki"
description = "…"
wiki_root = "wiki"             # 内容目录（相对 repo 根；保留字 inbox/raw/schemas 禁用）

[types.concept]                # 显式类型注册/覆盖
schema = "schemas/concept.json"
description = "…"

# 可覆盖段（◈ 标记的）：defaults / read / validation / ingest /
# graph / history / suggest / search / lint / redact
```

## 读写

- CLI：`config get <key>` / `config set <key> <value> [--global]` / `config list [--global]`
- MCP：`wiki_config {action: get|set|list, key, value, global, wiki}`
- dot-key 全集见 `internal/wiki/config.go` 的 `GetConfigValue` / `SetGlobalConfigValue` / `SetWikiConfigValue`；global-only 键（index/serve/logging/watch）在 wiki 级 set 时报 `global-only key — use --global`
- `search.status.<name>` 任意自定义状态乘子均可

## 环境变量

| 变量 | 作用 |
|---|---|
| `LLM_WIKI_CONFIG` | 配置文件路径 |
| `LLM_WIKI_LOG_LEVEL` | serve 日志级别（debug/warn/error，默认 info） |
| `LLM_WIKI_EMBEDDING_API_KEY` | 覆盖 `[embedding] api_key`（避免明文落盘） |
