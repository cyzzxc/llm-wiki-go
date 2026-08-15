# 架构

## 分层

```
cmd/llm-wiki          CLI 解析 + 输出格式化（text/json/llms）
      │
internal/mcpserver    MCP 传输适配（24 工具 + resources + 通知）
internal/acpserver    ACP 传输适配（JSON-RPC + 工作流）
internal/watch        fsnotify 事件 → OpsIngest
      │
      ▼
internal/wiki         ops 层（Ops* 函数）：业务入口，副作用归属地
      │
      ▼
域模块：slug / frontmatter / page / links / config / gitx /
        schema / index / search / graph / ingest / spaces / engine
```

规则：**传输层零业务逻辑**。`mcpserver` 只做参数解包 → 调 `Ops*` → 格式化输出；`commands.go` 同理。所有副作用（索引刷新、图缓存失效、edge-target 校验、git 提交）在 ops 层或更深的域模块里。

## 引擎与空间

```go
WikiEngine { mu RWMutex; State: {Config, ConfigPath, StateDir, Spaces: map[name]*SpaceContext} }
SpaceContext { Name, WikiRoot, RepoRoot, TypeRegistry, IndexSchema,
               IndexManager, Tokenizer, GraphCache, Resolved,
               communityMu/Gen/Stats/Map/LocalCnt }
```

- `BuildEngine(configPath)`：读全局配置 → 逐 entry `MountSpace`。挂载失败的 wiki 记 warning 跳过（坏 schema 是硬错误，不静默降级——与原版一致）。
- `MountSpace` 流程：`BuildSpace`（编译 schemas → registry + index schema）→ `Resolve`（全局+每 wiki 配置合并）→ 构造 tokenizer → 首建/增量重建索引 → `Open()`（载入 gob + state.toml）→ 图缓存（可选 WarmStart 快照）。
- 热挂载：`MountWiki/UnmountWiki/SetDefault` 供 spaces_* 工具在运行中的服务上调用；`OpsSpacesCreate` 等会回读磁盘配置再挂载。

## 并发模型

- `WikiEngine.mu`：读写锁保护 Spaces 映射。读操作（search/list/graph）拿 RLock 解引用 `*SpaceContext` 后即释放，长操作不持锁。
- `IndexManager.mu`：保护 index/state/generation。`Searcher()` 返回的 `*SearchIndex` **构建后不可变**——`Update/Rebuild` 生成新实例整体替换指针，读侧无锁。
- `GraphCache.mu` + `SpaceContext.communityMu`：图与社区缓存各自串行化，均以 `Generation()` 为代数键。
- ACP `outMu`：stdout 通知互斥；会话各自持 `cancelled atomic.Bool`。

## 进程生命周期差异（对齐原版的关键约束）

| | CLI | MCP/ACP 服务 |
|---|---|---|
| 生命周期 | 单发进程 | 常驻 |
| tokenizer | 每次冷加载（~400ms 词典） | 一次加载复用 |
| `Generation()` | 进程内单调 | 跨重启归零 → **缓存键必须用 `LastCommit()`** |
| 内存索引 | `Open()` 读 gob | 增量更新在内存演进 + 持久化 |

## 数据流：检索

```
query ──▶ tokenizer.Tokens（与索引同源）
       ──▶ 遍历匹配页（type 过滤 / 排除 section）
       ──▶ BM25(tf, df, len, avg) × status乘子 × confidence乘子
       ──▶ 排序截断 top_k ──▶ 摘要（首个命中词窗口 + <b> 高亮）
分面：type（未过 type 滤）/ status、tags（过滤后）── 与原版语义一致
```

## 数据流：图

```
SearchIndex.Docs ──▶ 节点（slug-sorted）
frontmatter 边字段（x-graph-edges 声明）──▶ 带关系标签的边
body [[wikilink]] / CommonMark 归一链接 ──▶ "links-to" 边
wiki:// 跨库目标 ──▶ 按需插入 external 占位节点
root+depth ──▶ BFS 双向子图
```

## 持久化物

| 文件 | 内容 | 可删 |
|---|---|---|
| `<repo>/wiki/**/*.md` | 用户数据（git 管理） | 否（用户所有） |
| `<repo>/wiki.toml`、`schemas/` | 每 wiki 配置与类型 | 否 |
| `~/.llm-wiki/config.toml` | 全局配置（wikis 注册表） | 否（丢了要重注册） |
| `~/.llm-wiki/indexes/<wiki>/index.gob` | BM25 索引 | ✅ 重建即恢复 |
| `~/.llm-wiki/indexes/<wiki>/state.toml` | 索引状态（built/pages/commit/types hash） | ✅（触发全量重建） |
| `~/.llm-wiki/snapshots/<wiki>/wiki-graph-*.gob` | 图快照（gzip，keep-N 轮转） | ✅ |
| `~/.llm-wiki/logs/` | 服务日志（daily 轮转） | ✅ |
