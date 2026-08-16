# Web 前端实施方案（已实现 ✅ 2026-08-16）

> 本文档自足：新会话无需旧上下文即可实施。方案已于 2026-08-16 经用户批准（"开工"），
> 因会话上下文限制转入新会话执行。仓库：`llm-wiki-go/`（remote `origin` =
> `https://github.com/cyzzxc/llm-wiki-go.git`，main 分支）。
>
> **实施结果**（与方案的偏差记录）：
> - 按计划落地：`internal/web/web.go` + `templates.go`、`--web[:PORT]` 接线、全部路由与测试、文档。
> - 偏差 1：首页/RSS 的最近更新数据经新增 ops 层函数 `OpsRecentPages` 提供（方案允许
>   "Searcher().Docs 遍历"，但 AGENTS.md 不变量 #1 要求 web 只调 Ops*，故收敛到 ops 层实现同一遍历）。
> - 偏差 2：`serve --web` 单独使用时抑制 MCP stdio（否则后台运行时 stdin EOF 会让整个 serve 退出——
>   冒烟测试发现的真问题；与 --http/--acp 的既有抑制规则一致）。
> - 偏差 3：raw HTML 的中和方式是 goldmark 默认的 `<!-- raw HTML omitted -->`（丢弃而非转义文本），
>   比"模板转义"更严格；`<script>` 载荷完全不进输出。
> - 偏差 4（预算未达）：二进制增量 +4.3MB（goldmark ~2.5MB + html/text/template ~1.5MB，MCP SDK
>   此前不引模板引擎），超出 §8 预估的 ≤ +1MB。goldmark 是本方案批准的核心选型（CommonMark 标准
>   实现），接受该成本；实测 24.7MB / strip 后 18.7MB。
> - 预算达成：单页 ~6KB（含内联 CSS，预算 ≤30KB）、零外联请求、JS ~230B、零新增持久化。

## 0. 当前状态（截至本文档写入）

- 已完成：整仓 Go 移植、中文分词、语义搜索、审计修复（4 个提交，最新含 `GitRecentCommits` 助手）
- **已为本次工作准备好的**：
  - `goldmark v1.8.5` 已加入 go.mod（Markdown 渲染，唯一新依赖）
  - `wiki.GitRecentCommits(repoRoot, limit) ([]HistoryEntry, error)` 已实现（gitx.go，首页活动日志用）
- 未开始：`internal/web/` 包、`--web` 接线、测试、文档

## 1. 需求定位（FLARE：先砍再写）

用户目标三条：**阅读 wiki 页面（中文排版）**、**人类可用的检索入口**（BM25/语义目前只有 CLI/MCP）、**总览健康度**。

参考站 `https://wiki.lkwplus.com/`（Digital Garden / astro-erudite 主题，MIT）结构：
顶栏（站名 + Concepts/Entities/Sources/Notes/Log/Graph 类型导航 + 搜索）、单栏正文、
hero 简介、统计徽章 pills（sources/entities/concepts/notes/links 计数）、
"Recently tended" 列表（标题/类型/日期）、Activity 反序 changelog、
页脚（版权 + GitHub/RSS）。衬线正文、暖米色系、疑似 Cmd+K 搜索浮层。

### 砍单（不做，含理由与加回条件）

| 砍掉 | 理由 | 加回条件 |
|---|---|---|
| SPA / 前端框架 | 纯阅读，服务端 HTML 是正解 | 无 |
| 编辑/保存 UI | 引擎信条：agent 写、人读；人改走 git | 用户明确要求 |
| Cmd+K 浮层 | 顶栏搜索框 + `/` 键聚焦（≤10 行 JS）替代 | 搜索高频反馈 |
| Mermaid 可视化 | mermaid.js ≈2MB 违反体积红线；图页用 llms 文本图 + 下载 .mmd/.dot | 真实可视化诉求 → build tag 可选嵌入 |
| 登录/权限 | 本机工具，默认绑 127.0.0.1 | 公网暴露 → 复用 MCP HTTP Host 白名单 + basic auth |
| Web 字体 | 中文 webfont 体积爆炸；本地字体栈 | 无 |
| 评论/阅读计数 | 臆测功能 | 无 |

保留：RSS（~30 行、零 JS）。

## 2. 技术选型

- Go stdlib `html/template` + `net/http`，全服务端渲染；CSS 单文件内嵌 `<style>`（~4KB）；**总 JS ≤ 1KB**
- goldmark 渲染 Markdown；wikilink 与相对链接在**渲染前**预处理（见 §4）
- 接入：`serve --web[:PORT]` flag（独立端口，默认 `127.0.0.1:8090`，与 stdio/MCP-HTTP/ACP 并存无冲突）
- 数据全部复用 `Ops*`（ops 层单一事实源不变量——见 AGENTS.md #1）

## 3. 路由表

| 路由 | 内容 | 数据来源（已实现） |
|---|---|---|
| `GET /` | hero + 类型统计 pills + Recently Tended（last_updated 倒序 10 条）+ Activity（`GitRecentCommits` 15 条） | `OpsStats` + 索引 `Searcher().Docs` 遍历 + `GitRecentCommits` |
| `GET /p/{slug}` | 正文 + type/status/confidence 徽章 + tags + 反向链接 | `ContentRead`（拿全文）+ `ParseFrontmatter`（徽章）+ `BacklinksQuery` |
| `GET /search?q=&mode=` | 结果列表：标题/摘要高亮/得分；mode=keyword\|hybrid（hybrid 需 [embedding]，未配置自动回退 keyword 并页面标注） | `OpsSearch`（excerpt 已是转义 HTML，模板用 `template.HTML` 安全注入） |
| `GET /list/{type}` | 单类型页面列表（导航落点；type 为空 = 全部按类型分组） | `OpsList` |
| `GET /graph` | llms 文本图 `<pre>` + 下载链接 | `OpsGraphBuild{Format:"llms"}` + `"mermaid"`/`"dot"` 变体 |
| `GET /graph.mmd`、`/graph.dot` | 原文下载（`text/plain; charset=utf-8`） | 同上 |
| `GET /feed.xml` | RSS 2.0，最近更新 20 页 | git log + `ContentRead` |

**安全**：slug 全走 `wiki.NewSlug` 校验（非法 → 404）；模板 autoescape；`q` 转义后回显。

## 4. Markdown 预处理（渲染前，按序）

1. **wikilink**：`[[slug]]` → `[slug 标题化](/p/slug)`、`[[slug|标题]]` → `[标题](/p/slug)`；
   **跳过 fenced code block**（按 ``` 行切分，只处理非代码段，~15 行）
2. **相对 CommonMark 链接**：`](./x.md)` / `](../dir/x)` 归一为 `/p/<slug>`——
   需先在 `internal/wiki/links.go` 把未导出的 `normalizeCommonmarkDest` 导出为
   `NormalizeCommonmarkDest(dest, sourceDir string) string`（一行改动 + 已有测试覆盖）
3. sourceDir 规则（与索引侧一致）：flat 页 = `path.Dir(slug)`；bundle 页（`<slug>/index.md` 存在）= slug 本身

goldmark 输出包 `<div class="md">`，CSS 做 `.md` 内的标题/列表/代码样式（衬线正文、代码块浅底）。

## 5. 视觉规格（对齐 astro-erudite）

- 布局：单栏 `max-width: 42rem; margin: 0 auto; padding: 0 1rem`；顶栏 sticky；
  页脚 = 版权 + repo/RSS 链接
- CSS 变量：
  ```css
  :root { --bg:#faf7f2; --fg:#2d2a26; --accent:#b4632c; --muted:#8a8178;
          --pill-bg:#f0e9df; --code-bg:#f3efe8; }
  @media (prefers-color-scheme: dark) {
    :root { --bg:#211e1b; --fg:#e8e2d9; --accent:#d99a5b; --muted:#9a9188;
            --pill-bg:#2d2925; --code-bg:#2a2723; } }
  ```
- 字体：正文 `"Songti SC","Noto Serif CJK SC",Georgia,serif`；标题/导航 `"PingFang SC","Noto Sans CJK SC",system-ui,sans-serif`；代码 `ui-monospace,Menlo,monospace`
- 组件：统计 pill（`类型 N` 小圆角标签，链到 /list/<type>）；列表行 = 标题左 + 类型标签 + 日期右；徽章 = type（accent 边框）/ status（灰）/ confidence 圆点（值→透明度）
- 顶栏搜索框：`<form action="/search">` GET；页面尾部内联 3 行 JS：按 `/` 聚焦搜索框

## 6. 实施步骤（顺序即依赖序）

1. `internal/wiki/links.go`：导出 `NormalizeCommonmarkDest`
2. `internal/web/web.go`（建议单文件 + 模板/样式用 const 字符串；`html/template` 一次 parse）：
   `func New(engine *wiki.WikiEngine, defaultWiki string) http.Handler`
   —— 6 个 handler + 类型导航条数据（`OpsStats().Types` 键排序）
3. `cmd/llm-wiki/serve.go`：解析 `--web[:PORT]`（复制现有 `--http` 的 flag 解析块，注意
   `parseFlags` 的 `boolFlags` 集合**不要**加 "web"——它像 --http 一样可带值）；
   传输日志 transports 数组加 `web :PORT`；web 与其它传输并存（独立 goroutine + errCh）
4. `cmd/llm-wiki/main.go` usageText 补 `serve [--web[:PORT]]`
5. 测试（见 §7）→ `gofmt -w . && go vet ./... && go test ./...`
6. 真机冒烟：`serve --web:8090` + curl 各路由 + 浏览器确认视觉
7. 文档：`docs/cli.md`（serve 行）、`docs/parity.md`（Go 增量行：web UI）、README 一段 + 本文档标记已完成
8. commit + push（origin/main）

## 7. 测试方案

`internal/web/web_test.go`，fixture 自建（wiki 包的 buildFixture 不导出）：
`SpacesCreate(tmp,"w",...,configPath,"")` → 写 2-3 页中文内容（含 `[[wikilink]]`、
相对链接、`<script>` 注入尝试、fenced code 里的 `[[x]]`）→ `OpsIngest` → `BuildEngine` → `web.New`。

| 断言 | 要点 |
|---|---|
| 首页 | 200、含统计 pill、含活动日志条目 |
| 页面渲染 | 200、wikilink 变 `/p/...` href、相对 .md 链接归一、`<script>` 被转义、代码块内 `[[x]]` 原样、中文正文存在 |
| 非法 slug | `/p/../etc` → 404（NewSlug 拒绝） |
| 搜索 | `?q=注意力` 结果含目标页；`?q=<script>` 注入被转义；hybrid 未配置时回退 keyword |
| 图 | `/graph` 含 "Key hubs"；`.mmd` 以 `graph LR` 开头 |
| RSS | `<?xml` 开头、含 `<item>` |

注意：测试跑 shell 需 `export HOME=<临时目录>`（fixture 依赖 `os.UserHomeDir`）。

## 8. 预算（完成定义）

| 指标 | 预算 |
|---|---|
| 单页重量（含内联 CSS） | ≤ 30KB，零外联请求 |
| 服务端渲染 p95 | keyword < 20ms；hybrid = 一次网关往返（页面标注延迟来源） |
| 二进制增量 | ≤ +1MB（goldmark） |
| 新依赖 / 新持久化 | 1 / 0 |

## 9. 本仓库关键不变量（实现时必守，详见 AGENTS.md / docs/invariants.md）

- ops 层单一事实源：web handler 只调 `Ops*`，不直接摸索引内部
- slug 一律过 `NewSlug`；HTML 一律靠模板 autoescape（excerpt 例外：已是转义 HTML）
- 零持久化新增；用户数据只有 markdown+git
- gob/缓存不变量与本次无关，但勿触碰 `Generation()`（用 `LastCommit()`）
