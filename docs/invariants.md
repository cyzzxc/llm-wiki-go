# 行为不变量

改动相关代码时必须保持的性质。每条附验证手段。风格对齐上游 `docs/invariants.md`。

## 数据主权

1. **用户数据只有 Markdown + git**。`~/.llm-wiki/` 之下的一切（索引/快照/日志/配置缓存）都是可删除的派生物或可重建的注册信息；删除 `indexes/<wiki>/` 与 `snapshots/<wiki>/` 后首个命令自动重建。
   - 验证：`rm -rf ~/.llm-wiki/indexes && llm-wiki search …` 仍出结果。
2. **页面即真相**。索引、图、统计永远可由 wiki 树全量重算；任何「索引有而文件无」的状态都是 bug（删除文件后必须从索引摘除——`Update` 的 Deleted 分支）。

## Slug 与路径安全

3. slug 非空、无前导 `/`、无 `..`/`.` 组件、无隐藏段（`.` 开头）、末段无扩展名；永远 POSIX `/` 风格。
   - 验证：`slug_test` 类用例 + `NewSlug` 拒绝表。
4. ingest 路径必须规范化后落在 wiki root 内（`path is outside wiki root`）。
5. `wiki_root` 校验：相对、无 `..`、非 `inbox|raw|schemas` 保留字、存在于 repo 内。

## 索引

6. **索引与查询同 tokenizer**。索引记录 `TokenizerName`；`Open()` 不匹配即弃用索引强制重建。改分词配置永不手工迁移。
7. **gob 解码后必须重算派生结构**（`SearchIndex.rebuildStats()`、`WikiGraph` 的 `adjOut/adjIn`）——gob 只编码导出字段。
   - 验证：`TestSearchAcrossProcessReload`（两个 `BuildEngine` 生命周期）。
8. **`persist()` 同步写 state.toml 与 index.gob**。`LastCommit()` 是增量更新锚点，跨进程必须非空（`Open()` 从磁盘装载 `m.state`）。
9. confidence 缺席是中性：乘子 1.0，绝不落 0.5；status 缺席用 `search.status.unknown`（默认 0.9）。
10. 检索结果确定性：得分同则按 slug 升序；文档序按 slug 升序（图节点序依赖）。

## Git 交互

11. porcelain 一律 `-c core.quotePath=false`（非 ASCII 路径不转义）。
12. nothing-to-commit 返回空串而非错误；`git log` 空 stderr 失败=空历史非错误。
13. ingest 默认提交信息格式 `ingest: <path> — +N pages, +N assets`；spaces create 为 `create: <name>`。

## 传输

14. **stdio 独占**：ACP 启用时 MCP stdio 不启动；HTTP 与 ACP 可并存。三者同抢 stdio = 静默吞消息（历史 bug）。
15. MCP 错误约定：`error: <msg>` 文本 + `isError: true`；缺参 `missing required parameter: <key>`；未知工具/动作的措辞与原版一致（agent 依赖这些字符串）。
16. 工具副作用通知：spaces_* → resources/list_changed；ingest（非 dry-run）→ 每页 resources/updated。均 best-effort。

## Ops 层

17. **ops 是唯一业务入口**：CLI 与 MCP 都只调 `Ops*`；索引刷新、图缓存失效、edge-target 校验等副作用属于 ops 层内部，调用方不补。

## 图

18. 缓存键：进程内用 `Generation()`；**跨进程对齐物用 `LastCommit()`**（generation 重启归零）。
19. 非默认 filter（root/types/relation）绕过图缓存直接构建；缓存只存全图。
20. 社区发现节点按 slug 排序（确定性）；pass 上限防振荡。

## 分词

21. 索引侧 `CutSearch`（全词+子词）与查询侧同一分词器、同一模式——任何一侧单独换 = 匹配断裂。
22. 拉丁内容永不经过词典（脚本路由），纯拉丁工作流零词典成本。

## 嵌入（语义搜索）

23. **嵌入默认关**：未配置 `[embedding]` 时引擎完全离线，行为与无此功能时一致。
24. **同模型硬约束**：`state.toml` 的 `embedding_model` 与当前配置不符 → 索引 stale、全量重建；查询与索引必须同网关同模型。
25. 嵌入文本构造确定性（title+summary+body 截断）；向量单位化存储，余弦=点积。
26. 嵌入失败不阻塞索引：降级为无向量（警告），keyword 模式不受影响。

## 校验

27. `default` 类型必须 require title+type（兜底不变量，违反即挂载失败）。
28. 标题缺失是硬错误；未知类型/ schema 违规按 strictness 降级。
29. dry-run 零写入（含 redact）；redact 只动 body 不动 frontmatter，行号 1-based。
