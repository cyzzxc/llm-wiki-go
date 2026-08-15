# 索引生命周期与持久化

`IndexManager` 拥有一个 wiki 空间的索引及其状态，负责全量重建、增量更新、部分重建、staleness 判定与持久化。

## 磁盘布局

```
~/.llm-wiki/indexes/<wiki>/
├── index.gob     # SearchIndex（Docs + 派生统计的源数据）
└── state.toml    # IndexState
```

`state.toml` 字段（与 Rust 原版 state.toml 对齐）：

```toml
schema_hash = '<TypeRegistry 全局 hash>'   # schemas 变更检测
built = '2026-08-16T01:32:32+08:00'        # RFC3339；缺席 = 从未构建
pages = 3
sections = 0
commit = '<构建时的 git HEAD>'              # 增量更新锚点
[types]
concept = '<per-type hash>'
...
```

**gob 陷阱**：`SearchIndex` 的 `bySlug/df/totalLen` 是未导出派生结构，gob 不编码。解码路径（`loadIndex`）必须调 `rebuildStats()` 重算——漏掉即跨进程检索静默为空（历史 bug，已固化回归测试 `TestSearchAcrossProcessReload`）。

## 全量重建 `Rebuild`

1. Walk wiki 树，逐 `.md` `IndexFile`（slug 推导 → frontmatter 解析 → 别名两遍归一 → 链接提取 → 加权词袋）
2. slug 排序（确定性，图节点序依赖它）
3. `NewSearchIndex`（重算 df/bySlug/totalLen）
4. `persist`：**先写 state.toml 再原子替换 index.gob**（tmp+rename）
5. 内存指针整体替换，`Generation()+1`

## 增量更新 `Update(lastCommit)`

1. `CollectChangedFiles(repoRoot, wikiRoot, lastCommit)`：合并两个 diff——`lastCommit..HEAD`（提交过的变更）+ 工作区 vs HEAD（porcelain，含 untracked）；工作区结果覆盖前者
2. 每个变更：Deleted → 摘除文档；否则重索引该文件
3. 重新统计 + 持久化 + 换指针

`OpsIngest` 在 ingest 提交后自动调用。changed-paths 匹配用 **wiki 相对路径**（`wiki/` 前缀已剥）；git porcelain 必须带 `-c core.quotePath=false`（中文路径否则被 C 转义）。

## 部分重建 `RebuildTypes(types)`

schema 变更只影响若干类型时：保留其它类型文档，仅重走匹配类型的文件，重写 state（pages/sections 置 0——与原版语义一致，由下次全量恢复精确计数）。

## Staleness 判定

`Staleness(repoRoot)` 返回四态，驱动「智能重建」：

| 态 | 条件 | 动作 |
|---|---|---|
| Current | commit 与 schema_hash 都一致 | 无 |
| CommitChanged | 仅 git HEAD 变 | `Update(last)` |
| TypesChanged | per-type hash 部分变 | `RebuildTypes(changed)`（失败降级全量） |
| FullRebuildNeeded | 无 state / 解析失败 / hash 集不一致 | `Rebuild` |

挂载时（`MountSpace`）：无 built → 首建；`index.auto_rebuild = true` → 按上表处理；否则 stale 只警告。

`Status(repoRoot)`（`wiki_index_status`）额外输出 `openable`（gob 存在且可解码）与 `queryable`。

## Open：跨进程恢复

`Open()` 把磁盘态载入内存：

1. `m.state` 为零时从 state.toml 读入（**必须**——否则 `LastCommit()` 为空串，增量锚点丢失，历史 bug）
2. `m.index` 为 nil 时读 gob；`TokenizerName` 与当前配置不符 → 弃用索引强制重建（分词器变更的自动迁移）

## 缓存代数与图

`Generation()` 每次索引变更 +1，进程内单调。图/社区缓存以它为键。**跨进程不稳定的键一律不用 generation**（进程重启归零），持久化对齐物用 `LastCommit()`。

## 删除与恢复

`indexes/<wiki>/` 整目录可随时删除——下次挂载判定「从未构建」自动全量重建。这是设计约束：任何新持久化物都必须可安全删除。
