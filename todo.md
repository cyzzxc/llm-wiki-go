# 待办（已完成）

来源：`57de6f3` 整仓移植 + `6b8100d` 语义搜索审查。全部处置于 `6bd0a30`（本提交）；回归测试见 `internal/wiki/regression_test.go` 与 `cmd/llm-wiki/main_test.go`。

## 严重（功能错误）— 全部修复 ✅

- [x] `OpsSchemaAdd` 不写 wiki.toml — 对齐 Rust 语义：仅当 schema **未**在 x-wiki-types 声明该类型时写 `[types.<名>]`；已声明则靠 schemas 发现。测试：TestSchemaAddWritesTomlWhenNotDeclared
- [x] git 重命名丢 slug — `diff --name-status` 的 `R100\told\tnew` 取新路径并把旧路径标 Deleted；porcelain `R old -> new` 同样补旧路径删除。测试：TestRenameUpdatesIndex（含中文路径）
- [x] `GitChangedSinceCommit` 缺 quotePath=false — 已补，两条 git 路径统一。
- [x] 图缓存跨进程吃过期快照 — 缓存键整体改为 `LastCommit()`；快照文件名内嵌构建 commit，`WarmStart` 后与当前 HEAD 不符即作废。测试：TestGraphCacheInvalidatedAcrossProcesses
- [x] Update/RebuildTypes 在 mu 内跑 embedding — 重构为 writeMu 串行写者 + mu 短持快照/安装 + 锁外嵌入/git IO。
- [x] watch 丢 Remove/Rename — 删除/重命名事件进入 debounce；触发改为整库 ingest（changed-paths 过滤保持 O(changes)，删除经 git add -A + 索引 Deleted 分支处理）。测试：TestDeleteRemovesFromIndex
- [x] bundle 资产 URI 缺 wiki 名 — `wiki://<wiki>/<slug>/<file>`。测试：TestAssetURIContainsWikiName
- [x] ingest 以 `filepath.Dir(wikiRoot)` 当 repo 根 — `Ingest` 显式接收 repoRoot。测试：TestCustomWikiRootCommitsToRepo

## 建议 — 全部修复 ✅

- [x] 布尔 flag 吞位置参数 — boolFlags 白名单（含 --semantic/--hybrid/--all/--redact/--force/--cross-wiki 等 21 个）。测试：TestParseFlagsBoolFlagsDoNotSwallowPositional
- [x] RebuildTypes 计数清零 — 统一走 countPagesSections + applyEmbedState。
- [x] WritePage 非法 slug 逃逸 — NewSlug 失败即错误，绝不落盘。测试：TestWritePageRejectsInvalidSlug
- [x] IPv6 Host 白名单 — net.SplitHostPort 解析 `[::1]:8080`。
- [x] `config get embedding.api_key` 回明文 — get 返回打码（首3+…+末4）；list 对 api_key 行掩码。测试：TestAPIKeyMasked
- [x] SearchAll 吞单库错误 — 全部库失败才报错，部分失败返回部分结果。
- [x] CLI usage 补 `--semantic|--hybrid`。
- [x] git commit 返回 `[branch hash] msg` — 改为提交后 `rev-parse HEAD` 全 hash。测试：TestCommitReturnsFullHash

## Ponytail — 已实施（除一项，见下）

- [x] 语义 excerpt 特判删除（makeExcerpt 统一：命中高亮 / 无命中正文头窗）
- [x] Update 嵌入块重复两次 → 一次
- [x] EmbeddingDims 三处推导重复 → embedDocs 返回维度 + applyEmbedState
- [x] embedWeightFrom 内联（移入非 keyword 分支，nil 安全）
- [x] GraphCache.Rebuild 删除（GetFresh 同构）
- [x] strPtr / `_ = os.DirFS` 等垫片删除 + 相应 import 清理
- [x] `var _ = assets.Schema` 垫片删除
- [x] sortStrings → sort.Strings；mapEqual → maps.Equal；手写 max → 内建 max
- [x] hasKey/containsPath 简化（map 直查 + Clean+Abs 归一比较）
- [x] filepathWalkMd 内联
- [x] DeletePage 死代码删除；MCP 注释 23→24

### 有意不实施（1 项）

- [ ] ~~删除 EmbedText（embedDocs 直传 d.Body）~~ — **保留**：嵌入文本确定性构造（title+summary+body）是 invariants.md #25 的既定不变量，标题是 wiki 页最强语义信号；改为仅 body 会静默改变全部向量的语义且 stale 检测无法感知（同模型、文本变），属行为变更而非机械削减。

## 有意保留（不算膨胀）

- GitPageHistory 用 bytes.Buffer 而非 gitOut（需区分空输出与 stderr 报错）。
- RedactBody 的 redactPattern + CompileRedactPatterns 分离（builtin/custom 合并 + disable 语义）。
- 语义搜索测试 + fake gateway。
- Remote 字段（已文档化）。
