# 页面类型系统

类型化 frontmatter 是 llm-wiki 的核心 DNA：每页声明 `type`，类型由 `schemas/*.json` 的 JSON Schema（draft 2020-12）定义，校验失败按严格度降级为警告或报错；图边从类型声明自动提取；索引字段类型由 schema 推导。

## Schema 文件的扩展协议

`<repo>/schemas/*.json`，除标准 JSON Schema 外识别四个扩展键：

| 键 | 形状 | 作用 |
|---|---|---|
| `x-wiki-types` | `{类型名: 描述}` | **注册类型**。没有此键的文件不注册任何类型 |
| `x-graph-edges` | `{字段: {relation, direction, target_types[]}}` | 声明边字段：frontmatter 里该字段的每个值成为一条带关系标签的有向边；`relation` 默认 `links-to`；`target_types` 空=不限 |
| `x-index-aliases` | `{别名: 规范名}` | 字段别名（如 skill 的 `name→title`、`description→summary`、`when_to_use→read_when`）——别名值在索引/校验时按规范名归位 |
| `x-keyword` | bool（属性上） | 数组字段标记为关键词型（可分面过滤） |

## 内置类型（随 `spaces create` 落盘，可编辑）

| 类型 | schema | required | 声明边 |
|---|---|---|---|
| `default` | base.json | title, type | —（未知类型的兜底） |
| `concept` / `query-result` | concept.json | title, type, **read_when** | sources→fed-by(源类型族)、concepts→depends-on(concept)、superseded_by→superseded-by |
| `paper` `article` `documentation` `clipping` `transcript` `note` `data` `book-chapter` `thread` | paper.json | title, type | sources→cites、concepts→informs、superseded_by |
| `doc` | doc.json | title, type | sources→informed-by、superseded_by |
| `skill` | skill.json | **name, description**, type | document_refs→documented-by(doc)、superseded_by；别名 name→title、description→summary、when_to_use→read_when |
| `section` | section.json | title, type | —（目录索引页） |

正文模板：`schemas/<type>.md` 优先，回退内嵌默认（concept/paper/doc/section/query-result）。

## 注册与覆盖

- 发现：`schemas/*.json` 按文件名排序逐个编译（`jsonschema` v6，draft 由 `$schema` 决定）
- `wiki.toml [types.<名>] {schema, description}` 显式注册/覆盖（可指向 schemas 外的路径）
- **base 不变量**：`default` 类型必须 require `title` 和 `type`——它是所有未知类型的兜底；缺失时注入内嵌 base.json，存在但不满足时硬错误
- 无任何 schemas → 使用内嵌默认集
- hash：per-type = sha256(schema路径 + 排序别名对 + 文件内容)；global = sha256(排序 per-type)——驱动索引 staleness 的 TypesChanged 判定

## 索引字段分类（`classifyField`）

固定字段预置：`slug`/`uri`/`body_links` 关键词、`body` 文本。其余属性按此表（**首见者胜**，跨 schema 不覆盖）：

| 属性形状 | 归类 |
|---|---|
| 出现在 `x-graph-edges` | Keyword |
| `type: string` 且有 `enum`/`const` | Keyword |
| `type: string` 其它 | Text |
| `type: boolean` | Keyword |
| `type: array` + `x-keyword: true` | Keyword |
| `type: array` + items 有 `enum`/`const` | Keyword |
| `type: array` 其它（如 sources/concepts 字符串数组） | Text* |
| `type: number`/`integer` | Numeric |
| object / 未知 | Text |

\* 与原版一致：无 x-keyword 的普通字符串数组归类 Text；但因 sources/concepts 等同时是边字段，实际按第一行落为 Keyword。落在 Text 的字段进 `TextVals`；Numeric 进 `NumericVals`；**不在索引 schema 里的 frontmatter 键**拼入 extra_text 追加 body（可检索）。

## 校验（ingest 时）

`ValidateType(fm, strictness)`：

1. `title` 必须非空（`name` 别名可替）——硬错误
2. `type` 缺席 → 警告 `missing field: type (defaulting to "page")`，按 `default` 校验
3. `type` 未注册 → strict 硬错误 `unknown type '...'`；loose 警告后按 `default` 校验
4. schema 违规 → strict 取首条硬错误；loose 每条降为警告

严格度配置：`[validation] type_strictness = "loose" | "strict"`（默认 loose，可每 wiki 覆盖）。

## 管理操作

- `schema list` / `schema show <t>` / `schema show <t> --template`（required 字段优先 + summary/status/last_updated/tags 的 frontmatter 模板）
- `schema add <t> <path>`：校验 JSON + x-wiki-types 含该类型 → 拷入 schemas/ → 必要时补 wiki.toml `[types.t]` → 重挂载并重建索引
- `schema remove <t>`：禁止删 `default`；支持 dry-run；可选删页面文件/删 schema 文件（仅当该文件声明 ≤1 类型）/清 wiki.toml 注册；自动提交 `schema remove: …`
- `schema validate`：逐文件 JSON/Schema 可解析性 + x-wiki-types 存在性 + 指定类型是否注册
