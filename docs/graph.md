# 概念图谱

## 构建 `BuildGraph`

从 `SearchIndex` 构建，无文件 IO。节点 = 索引文档（slug 升序，确定性）；边来自两个来源：

1. **声明边**：frontmatter 字段在类型 schema 的 `x-graph-edges` 里声明（如 concept 的 `sources` → `fed-by`、`concepts` → `depends-on`、paper 的 `sources` → `cites`、skill 的 `document_refs` → `documented-by`、`superseded_by` → `superseded-by`）
2. **正文边**：body `[[wikilink]]` 与归一化后的 CommonMark 链接 → 通用 `links-to`

目标解析：

- 本地 slug 在索引中 → 正常边
- `wiki://name/slug` 跨库目标 → 按需插入 `external: true` 占位节点（title 存原始 URI）
- 本地 slug 不存在 → 无边（由 lint 的 broken-link 报告）

过滤 `GraphFilter{root, depth, types, relation}`：types 过滤节点；relation 过滤边；root+depth 在建图后做 **BFS 双向子图**（出入邻居都走，深度耗尽为止）。

## 跨库合并 `MergeGraphs`

每库先建全图，再合并为一张：节点键 `wikiname/slug`；单库图里的 external 占位（title=wiki:// URI）重解析——目标库已挂载则连接成真边，未挂载保留占位。

## 社区发现（Louvain phase 1）

- 输入：对称化的本地（非 external）邻接；节点按 slug 排序保证确定性
- 贪心模块度优化：每轮每个节点移到带来最高增益的邻接社区，直到一轮无移动；pass 上限 `max(10·n, 100)` 防振荡（对齐原版）
- 社区 id 按 slug 序归一为连续 0..k
- 产出 `CommunityStats{count, largest, smallest, isolated(社区≤2 的页)}` 与 slug→社区映射
- 门槛：本地节点 < `graph.min_nodes_for_communities`（默认 30）不跑
- 消费方：`wiki_stats` 的 communities 字段、`wiki_suggest` 策略 4（same knowledge cluster，取 `community_suggestions_limit` 个字母序 peers）

## 结构拓扑（stats + lint）

| 算法 | 用途 | 复杂度 |
|---|---|---|
| Tarjan 割点 | lint `articulation-point`：删该页图断开 | O(V+E) |
| Tarjan 桥 | lint `bridge`：删该边图断开 | O(V+E) |
| BFS 离心率 | stats 直径/半径/中心；lint `periphery`（最大离心率=边缘页） | O(V·(V+E)) |

直径族算法受 `graph.max_nodes_for_diameter`（默认 2000）保护，超限输出 `structural_note` 而非硬算。`structural_algorithms = false` 可整体跳过。

## 渲染器

| 格式 | 内容 |
|---|---|
| `mermaid`（默认） | `graph LR`；节点 `[title]:::type`，边 `-->|relation|`；内置 8 个已知 type 的配色 + external 虚线样式 |
| `dot` | Graphviz `digraph wiki`；本地节点以 slug 为 id，external 以 URI 为 id |
| `llms` | 自然语言：节点/边/type 组计数、top-5 hubs、关系分布、孤立节点、外部引用 |

`--output` 落盘；`.md` 后缀自动包 `WrapGraphMD`（generated frontmatter + 代码围栏）。

## 缓存与快照

- `GraphCache` 按 `IndexManager.Generation()` 缓存全图；非默认 filter 请求**绕过缓存**直接构建（对齐原版）
- 快照（`graph.snapshot = true`，默认开）：gob+gzip 写入 `~/.llm-wiki/snapshots/<wiki>/wiki-graph-<UnixNano>.gob`，保留最新 `snapshot_keep`（默认 3）份；挂载时 `WarmStart` 载入最新
- 快照格式名兼容原版（`bincode` / `bincode+lz4` / `bincode+zstd`），编码一律 gob+gzip——纯内部缓存，无互操作（差异见 parity.md）
- gob 解码后必须重建 `adjOut/adjIn`（同索引的派生结构陷阱）

## 指标

`ComputeMetrics`：节点/边数、orphan（零度节点）、平均连接度（2E/V）、密度（E/(V(V−1))）——`wiki_stats` 与原版同源。
