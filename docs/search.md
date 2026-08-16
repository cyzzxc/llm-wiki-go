# BM25 检索设计

Go 版用手写的内存 BM25 替代 tantivy。本文档给出精确语义——包括与 tantivy 的已记录偏差。

## 文档模型

`IndexDoc`（一个页面）：

| 字段 | 来源 | 用途 |
|---|---|---|
| `Slug/URI` | 路径推导 | 主键、跨库 URI |
| `Title/Summary` | frontmatter | 检索字段（加权） |
| `Type/Status` | frontmatter | 过滤 + 得分乘子 |
| `Tags` | frontmatter tags | 分面 |
| `Confidence *float64` | frontmatter（缺席=nil） | 得分乘子（缺席=1.0 中性） |
| `Fields map[string][]string` | 关键词型字段（sources/concepts/read_when/…） | 图边、lint、suggest |
| `TextVals map[string]string` | 文本型字段（last_updated/tldr/…） | lint stale、模板 |
| `NumericVals map[string]float64` | 数值字段（confidence 等） | — |
| `BodyLinks` | 正文链接提取 | 图、backlinks、lint |
| `Body` | 原文正文 | 摘要窗口、export |
| `TF map[string]int`、`Len` | 加权词袋 | BM25 |

## 加权词袋（与 tantivy 的偏差 #1）

tantivy 对 title/summary/body 分别建字段、QueryParser 跨字段或查询。Go 版合并为单一加权词袋：

```
tf[token] = 3×(title tokens) + 2×(summary tokens) + 1×(body tokens)
```

实践效果：标题命中的页显著领先（原版行为近似），实现只有一张倒排表。索引字段（`Fields`/`TextVals`）**不参与** BM25——与原版一致（查询字段仅 title/summary/body；schema 外的额外 frontmatter 由原版拼进 extra_text 追加 body，Go 版对齐此行为属于 `BuildIndexDoc` 的 extra 路径）。

## BM25 打分

标准 Okapi BM25，参数与 tantivy 默认一致（k1=1.2, b=0.75）：

```
score(D,Q) = Σ_t  idf(t) · tf(t,D)·(k1+1) / (tf + k1·(1−b+b·|D|/avgdl))
idf(t)     = ln(1 + (N − df + 0.5)/(df + 0.5))
```

最终得分再乘两个乘子（对齐原版 collector 的 tweak_score）：

```
final = bm25 × status_mult × confidence_mult
status_mult: frontmatter status 查 [search.status] 表；
             缺席/未知名 → 表里的 unknown 项（默认 0.9）
confidence_mult: 页声明 confidence 则用其值（clamp [0,1]），否则 1.0（中性，绝不伪造）
```

默认 status 表：

```toml
[search.status]
active   = 1.0
draft    = 0.8
archived = 0.3
unknown  = 0.9
```

排序：得分降序，平分按 slug 升序（确定性）。跨库搜索（`cross_wiki`）合并后同规则重排、截断 top_k。

## 过滤与分面

- 过滤：`type` 精确匹配；默认**排除** `type: section`（`include_sections` 打开）。
- 分面语义（对齐原版）：
  - `type`：应用了查询但**未应用 type 过滤**的集合计数
  - `status` / `tags`：应用了全部过滤的集合计数
  - `tags` 取 top `facets_top_tags`（默认 10；0=全部）；跨库合并后重截断

## 摘要（excerpt）

- 找 body 中**最早出现**的任一查询词位置，取 ±80 字节窗口（rune 对齐），HTML 转义后把窗口内出现的查询词包 `<b>…</b>`（tantivy snippet.to_html 风格）。
- 窗口越界补省略号 `…`；无命中词时取开头窗口。
- 与 tantivy 的偏差 #2：tantivy 按查询 AST 精确高亮；Go 版按 token 子串大小写不敏感匹配（CJK 子串≈词元；拉丁词可能部分匹配——可接受的近似）。

## 查询语言

无 tantivy QueryParser 语法（`field:term`、`+must`、通配）。查询串整体分词后 OR 语义；解析失败的宽容路径不存在于 Go 版（原版的 lenient 解析主要为兜底语法错误）。若未来需要 field 查询，在 `OpsSearch` 前解析 `type:` 前缀即可。

## 语义检索（embedding）与混合模式

Go 版增量功能（Rust 原版无）。`[embedding]` 配置并 `index rebuild` 后可用：

| 模式 | 排序依据 | 说明 |
|---|---|---|
| `keyword`（默认） | BM25 | 行为与本文件前文一致；未配置嵌入时的唯一模式 |
| `semantic` | 余弦相似度（单位向量点积） | 只含带向量的文档；查询经同一网关嵌入。可召回同义改写、跨语言等**无词面重叠**内容 |
| `hybrid` | `w·cos + (1−w)·bm25/max(bm25)` | BM25 按结果集最大值归一到 [0,1]；负余弦按 0 计；无向量的文档仅靠关键词侧得分 |

- 嵌入文本：`title + "\n" + summary + "\n" + body`，按 `max_text_chars`（rune）截断——确定性，重建可复现。
- 向量在索引时**批量**生成（`batch_size` 条/请求），单位化后随 index.gob 持久化（4096 维 float32 ≈ 16KB/页）。
- 三种模式都套用 status/confidence 乘子；摘要统一走关键词路径：命中查询词则高亮窗口，无词面命中则取正文开头（semantic 查询无词面重叠时即后者）。
- **同模型硬约束**：`state.toml` 记录 `embedding_model`；配置换模型 → 索引判定 stale → 全量重建（向量空间不可混用）。
- 未配置嵌入而请求 semantic/hybrid → 错误 `semantic search not configured — set [embedding] in config and rebuild the index`。
- 成本参考（AxonHub / qwen3-embedding-8b）：~1.4s/条，批量摊薄；2 页库重建 2.2s；semantic/hybrid 查询每次 1 次网关往返。

## llms 渲染

- `render_search_llms`：`- [title](uri): summary` 行列表，无得分无摘要块。
- `render_list_llms`：按 type 分组（组内 confidence 降序、title 升序），archived 删除线，尾部分页脚注。

## backlinks

`BacklinksQuery`：扫描全部文档的 `BodyLinks` 精确匹配 slug，返回 `{slug,title}` 按 slug 升序。`wiki_content_read --backlinks` 输出 `{content, backlinks}` JSON。

## 性能（实测）

| 规模 | 单查询延迟（内存） |
|---|---|
| 1,000 页 | 0.38ms |
| 10,000 页 | 5.8ms |

复杂度 O(N·|query|)。十万页级若成为瓶颈，再加倒排索引按词筛文档（当前规模不值得，FLARE）。
