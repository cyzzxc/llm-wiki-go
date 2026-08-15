# Ingest 流水线与脱敏

`wiki_ingest`（及 watch 自动触发）是「文件 → 校验 → git → 索引」的唯一正门。

## 流程

```
path（文件或目录，相对 wiki root）
  │
  ▼ 边界检查：存在 + 规范化后在 wiki root 内（防逃逸）
  ▼ walk：.md → 待校验；其余文件 → assets_found
  │     （changed-paths 过滤：非本次 git 变更的 .md 跳过并计 unchanged）
  ▼ 逐文件 validateFile：
  │     1. CRLF/CR → LF 归一
  │     2. （可选）redact 脱敏 body
  │     3. frontmatter 解析：空 → 警告 no frontmatter found（仍计数）
  │     4. ValidateType（strictness 决定警告/硬错误）；硬错误中止该文件
  ▼ auto_commit → git commit "ingest: <path> — +N pages, +N assets"
  ▵ （OpsIngest 层）IndexManager.Update 增量重索引
  ▵ validateEdgeTargets：边目标类型不在 target_types → 追加警告
```

dry-run：全量校验、零写入、零提交。changed-paths 只在非 dry-run 生效（dry run 校验一切）。

## changed-paths 语义

`CollectChangedFiles` 合并：`lastCommit..HEAD` diff + 工作区 porcelain（含 untracked），工作区覆盖提交视图。ingest 只校验集合内文件——大库日常写入只花变更页的时间。

## 脱敏（redact）

`wiki_ingest --redact` 显式开启（**有损**：原文被替换后不可恢复——先跑 dry-run 看报告）。

- 只处理 **body**（frontmatter 原样保留），逐行替换，报告 1-based 行号
- 行尾风格（有/无尾换行）保持

内置 6 模式：

| 名称 | 正则 | 替换 |
|---|---|---|
| github-pat | `ghp_[A-Za-z0-9]{36}` | `[REDACTED:github-pat]` |
| openai-key | `sk-[A-Za-z0-9]{48}` | `[REDACTED:openai-key]` |
| anthropic-key | `sk-ant-[A-Za-z0-9\-]{90,}` | `[REDACTED:anthropic-key]` |
| aws-access-key | `AKIA[0-9A-Z]{16}` | `[REDACTED:aws-access-key]` |
| bearer-token | `Bearer [A-Za-z0-9\-._~+/]{20,}` | `[REDACTED:bearer-token]` |
| email | 标准邮箱 | `[REDACTED:email]` |

配置（全局或每 wiki）：

```toml
[redact]
disable = ["email"]                # 关闭内置模式
[[redact.patterns]]                # 自定义模式（无效正则跳过）
name = "internal-token"
pattern = "INT-[0-9]{10}"
replacement = "[REDACTED:internal]"
```

## 输出

CLI 文本：

```
Ingested: 3 pages, 1 unchanged, 0 assets, 2 warnings, 1 redactions
  warn: <path>: <详情>
  redacted: <slug> line 12 [github-pat]
Commit: <hash>        # 或 (dry run — nothing committed)
```

MCP `wiki_ingest`：JSON 报告（pages_validated / assets_found / warnings / commit / unchanged_count / redacted）；非 dry-run 额外向订阅会话发 `notifications/resources/updated`（每个触及页的 `wiki://` URI）。

## watch 自动 ingest

`serve --watch` / `watch` 命令：fsnotify 监听所有已挂载空间的 wiki 树，debounce（默认 500ms，`[watch] debounce_ms`）后对变更路径跑同一 OpsIngest；ACP 启用时向同 wiki 的空闲会话推送提示。已知限制：运行中新建的子目录需重建 watcher 才被监控。
