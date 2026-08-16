# CLI 参考

全局 flag（可出现在命令前或后）：`--wiki <NAME>`（目标库）、`--config <PATH>`（配置文件；缺省 `$LLM_WIKI_CONFIG` → `~/.llm-wiki/config.toml`）。
`--format json` 触发 JSON 输出（唯一 JSON 触发值）；`--format llms` 仅 search/list 支持。退出码：成功 0；业务错误 1；用法错误 2；**lint 有 error 级发现时退出 1**。

```
llm-wiki [flags] <command> …
```

## 空间

```
llm-wiki spaces create <path> --name <N> [--description D] [--force] [--set-default] [--wiki-root DIR]
  # → Created wiki "N" at P / Wiki "N" at P already exists / Registered in <config> / Initial commit: create: N
llm-wiki spaces register <path> --name <N> [--description D] [--wiki-root DIR]
llm-wiki spaces list [name] [--format json]
  # 文本：表头 "  NAME         PATH … description"；* = 默认库；描述空显示 —
llm-wiki spaces remove <name> [--delete]     # 默认库拒删
llm-wiki spaces set-default <name>
```

## 配置

```
llm-wiki config get <key>                     # 裸值
llm-wiki config set <key> <value> [--global]  # → Set k = v (global|wiki: N)
llm-wiki config list [--global]               # global=原始 TOML；否则解析后 JSON
```

## 内容

```
llm-wiki content read <uri> [--no-frontmatter] [--list-assets]
  # 页面原文 print（无尾换行）；资产列表逐行；二进制 → 报错退出 1
llm-wiki content write <uri> [--file PATH]    # 缺省读 stdin → Wrote N bytes to P
llm-wiki content new <uri> [--section] [--bundle] [--name T] [--type T] [--dry-run]
  # dry-run → Would create {section|bundle|flat} at wiki://…；成功 → Created: wiki://…
llm-wiki content commit [slugs…] [--all] [-m MSG]
  # → hash / Nothing to commit；无 slugs 且无 --all → 报错
```

## 检索

```
llm-wiki search <query> [--type T] [--no-excerpt] [--top-k N] [--include-sections] [--cross-wiki] [--semantic|--hybrid] [--format json|llms]
  # --semantic 向量召回 / --hybrid 混合排序（需 [embedding] 配置，见 docs/search.md）
  # 文本块：slug:/uri:/title:/score:/(excerpt:) + 空行
llm-wiki list [--type T] [--status S] [--page N] [--page-size N] [--format json|llms]
  # 文本行 "%-40s %-16s %-8s %s"（slug/type/status/title）+ "Page x/y (N total)"
llm-wiki suggest <slug> [-n N] [--format json]
  # "%-40s %.2f  title" + "  → field  (reason)"；空 → No suggestions.
```

## 摄取与索引

```
llm-wiki ingest <path> [--dry-run] [--redact] [--format json]   # 详见 docs/ingestion.md
llm-wiki index rebuild [--dry-run] [--format json]
  # dry-run → Would index N pages from <root>；否则 Indexed N pages in Xms
llm-wiki index status [--format json]
  # wiki:/path:/built:(never)/pages:/sections:/stale:/openable:/queryable:
```

## 图与统计

```
llm-wiki graph [--format mermaid|dot|llms] [--root SLUG] [--depth N] [--type a,b] [--relation R] [--output F] [--cross-wiki]
  # 无 --output 打印渲染；有 → Wrote graph to F（.md 包裹 frontmatter）
llm-wiki stats [--format json]
  # wiki — N pages, M sections / types: / status: / orphans: / graph: / staleness: / index:
llm-wiki lint [--rules a,b] [--severity error|warning] [--format json]
  # [severity] slug — message (rule) 逐行 + 汇总；errors>0 退出 1；空 → wiki N: ok (no findings)
llm-wiki history <slug> [-n N] [--no-follow] [--format json]
  # "hash7  date10  message40  author"
```

## 导出与解析

```
llm-wiki export [--path P] [--format llms-txt|llms-full|json] [--status active|all]
  # → Exported N pages (B bytes) → P
llm-wiki resolve <uri>     # JSON {slug, wiki, wiki_root, path, exists, bundle}
```

### export 格式

- **llms-txt**（默认）：`# name` + 页数；按 type 分组（组数降序/名升序）`## type (n)` + `- [title](uri): summary`（archived 删除线在 llms-full/list 里，txt 不含 archived 除非 `--status all`）
- **llms-full**：同头；逐页 `---` 分隔 + `# [title](uri)` + `_summary_` + 剥 frontmatter 的正文
- **json**：`[{slug, uri, title, type, status, confidence?, summary, frontmatter{额外字段}, body}]`

## Schema

```
llm-wiki schema list [--format json]                     # name16 description
llm-wiki schema show <type> [--template]                 # schema 原文 / frontmatter 模板
llm-wiki schema add <type> <schema-path>                 # → copied to … [, added [types.t] to wiki.toml][, search index rebuilt]
llm-wiki schema remove <type> [--delete] [--delete-pages] [--dry-run]
llm-wiki schema validate [type]                          # ok / 问题行（退出 1）
```

## 服务

```
llm-wiki serve [--http[:PORT]] [--web[:PORT]] [--acp] [--watch] [--dry-run]
  # --dry-run → Would start: [stdio] / [acp] [http :PORT] [web :PORT] [watch]
  # 传输互斥规则：ACP 独占 stdio；HTTP 可与 ACP 并存；--web 抑制 stdio（后台运行 stdin EOF 不致退出）；否则 MCP stdio
  # ctrl-c / SIGTERM 优雅退出（"server stopped"）
llm-wiki serve --web            # 只读 Web UI @ http://127.0.0.1:8090（可与 --http/--acp/--watch 叠加）
  # 路由：/ 首页（统计 pills + Recently tended + git Activity）、/p/<slug> 页面、
  #        /search?q=&mode=keyword|hybrid|semantic、/list[/<type>]、
  #        /graph（+ /graph.mmd /graph.dot 下载）、/feed.xml（RSS）
llm-wiki watch            # 独立 watcher：Watching for changes (ctrl+c to stop)…
```

## 日志

```
llm-wiki logs tail [--lines N]     # 最新日志尾 N 行（默认 50）
llm-wiki logs list                # 文件路径列表 / no log files
llm-wiki logs clear               # removed N log file(s)
```

## 其它

```
llm-wiki info [--format json]     # version/config/spaces/default wiki
llm-wiki version / help
```
