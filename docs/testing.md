# 测试与冒烟手册

## 单元 + 集成测试

```console
go test ./...                     # 全部
go test ./internal/wiki -v        # 中文语料集成（见下）
go test ./internal/tokenizer -v   # 分词器
```

注意：fixture 依赖 `os.UserHomeDir()`。从 shell 跑时先隔离 HOME：

```console
export HOME=$(mktemp -d)
```

### 集成测试覆盖（internal/wiki/wiki_test.go）

`buildFixture`：临时 HOME + `spaces create` + 三页中文/混合内容 + git 提交 + ingest + 索引。

| 测试 | 断言 |
|---|---|
| TestChineseSearch | 「注意力机制」「混合专家模型」「门控网络 路由」「recurrent gating」各自 top1 命中正确页 + 摘要非空 |
| TestSearchAcrossProcessReload | **跨进程回归**：第二个 `BuildEngine` 后检索仍命中（gob 派生结构重建） |
| TestListAndFacets | total / type / status 分面计数 |
| TestGraphEdgesAndRender | 节点数、depends-on 边、llms 渲染含 Key hubs |
| TestStats | pages / index 非 stale / 直径 ≥1 |
| TestLintRules | missing-fields + 结构化规则（割点/桥/边缘）出现 |
| TestSuggest | 关联页建议命中 |
| TestExportFormats | llms.txt 头与内容、json 页数 |
| TestContentLifecycle | new → write → read(无 frontmatter) → resolve → commit → history 全链 |
| TestIngestDryRunAndRedact | dry-run 零写入；redact 后密钥消失且报告行号 |
| TestIncrementalIndexUpdate | 新页 ingest 后跨进程可检索、index 不 stale |
| TestTokenizerPersistence | 索引记录 tokenizer 名 |
| TestBacklinks | 反向链接命中 |

### 基准

```console
go test ./internal/wiki -bench BenchmarkSearch -run=XXX
# 中文 1k 页 ≈ 0.38ms/查询；10k 页 ≈ 5.8ms/查询
go test ./internal/tokenizer -bench . -run=XXX
```

## 冒烟（发布前人工过一遍）

### 1. CLI 全流程

```console
export HOME=$(mktemp -d)
llm-wiki spaces create /tmp/w --name w --set-default
# 写几页中文 markdown 到 /tmp/w/wiki/
llm-wiki ingest .
llm-wiki search "某中文词"
llm-wiki stats && llm-wiki graph | head && llm-wiki lint
llm-wiki export --path /tmp/llms.txt
```

### 2. MCP stdio

```console
(printf '%s\n%s\n%s\n' \
 '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' \
 '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
 '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"wiki_search","arguments":{"query":"中文查询"}}}'; sleep 3) | llm-wiki serve
```

检查：initialize 返回 `llm-wiki/0.1.0`；tools/list 24 个工具；wiki_search 返回 JSON。
（注意尾部 `sleep`：stdio EOF 即关服，需给响应留时间。）

### 3. MCP HTTP

```console
llm-wiki serve --http:18099 &
curl -s -X POST localhost:18099/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'
# 期望：SSE 流 + Mcp-Session-Id 头
```

### 4. ACP

见 docs/acp.md 末尾 python 驱动脚本。检查：initialize agentInfo、session/new、
`llm-wiki:research <中文查询>` 流式输出 tool_call / tool_call_update / agent_message_chunk，
最终 `{"stopReason":"end_turn"}`。

### 5. watch

```console
llm-wiki watch &
echo '…新页面…' > /tmp/w/wiki/new.md
# 等 debounce(500ms)+ingest，观察 "ingested …" 输出；ctrl-c 退出
```

## 质量门

```console
gofmt -l .        # 空
go vet ./...      # 无输出
go test ./...     # 全绿
```

## 常见回归陷阱（历史 bug → 对应测试）

| 症状 | 根因 | 守门测试 |
|---|---|---|
| 跨进程检索为空 | gob 未编码派生结构 | TestSearchAcrossProcessReload |
| 增量更新不生效 | state.toml 未持久化 / `Open()` 未装载 | TestIncrementalIndexUpdate |
| 中文文件不被 ingest | porcelain 缺 quotePath=false | 手工冒烟 |
| ACP 消息被吞 | MCP stdio 与 ACP 并存 | 冒烟 §4 |
| 写 panic | changed map 未初始化 | TestIngestDryRunAndRedact 路径 |
