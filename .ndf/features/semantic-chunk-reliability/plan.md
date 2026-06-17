# 实施计划 Plan：semantic-chunk-reliability

> S3 · 2026-06-17 · 串行(多数 task 触及 ingest 重叠区,主 session 自实现 + 每 task 双 Sonnet reviewer)

## 依赖 / 顺序

```
T0 复现测试(RED) → T1 留痕(含加列+migration) → T2 永不失败加固 → T3 好兜底 → T4 语义可靠性
```
T1 是 keystone(复现转 GREEN)。T2/T3/T4 相对独立,但都在 ingest 区,串行实现。

## T0：复现测试(Rule 11,分支第一个 commit,先于任何实现)

- 客户上报"语义经常不工作"→ 第一个 commit 必须是失败复现测试,`test(qa):` 前缀。
- **位置**：`internal/pkg/retrieval/ingest/pipeline_strategy_test.go`。
- **断言**：用 fake splitter 模拟"语义不可用→兜底",跑 pipeline(或 splitter 链)→ 断言 (a) 能拿到 strategy="rule_fallback";(b) 上传仍成功、chunk 非空。**修前 FAIL**（无 strategy 暴露机制/字段）。
- 修复后(T1/T2)转 GREEN,永久留存。

## T1：留痕(可观测)

- **文件**：`splitter_adapter.go`(加 `SplitWithStrategy`)、`hybrid_splitter.go`(复用 `SplitWithDetails`)、`pipeline.go`(改调+持久化)、`model/knowledge_document.go`(加 2 列)、`migrations/<ts>_add_split_strategy.sql`(新)、store 层(写入方法)。
- **验收**：
  - 单测:splitter 链 `SplitWithStrategy` 返回正确 strategy(semantic/rule_fallback/no_split)。
  - 单测:pipeline 把 strategy+detail 写进 doc 记录;写入失败不阻断入库(只日志)。
  - migration 用 information_schema 守卫(MySQL 8 无 ADD COLUMN IF NOT EXISTS)。
  - `go test ./internal/pkg/retrieval/... ./internal/numind/...` + `task lint` 退 0。

## T2：永不失败加固 + 回归测试

- **文件**：`pipeline.go`(审计 fail 点)、`pipeline_strategy_test.go`(扩)。
- **验收**：
  - 单测:语义整段不可用(fake)→ pipeline 处理完 doc=COMPLETED、chunk 非空、strategy=rule_fallback、无 err。
  - 审计确认 `p.fail` 只用于真正硬错(解析失败/空文档),不因语义问题触发。
  - 退 0。

## T3：兜底切得好

- **文件**：`splitter_adapter.go`(NewCompatibilitySplitter 的 RuleConfig)。
- **改**：MaxChunkSize 6000→1800;保留 jieba+markdown。
- **验收**：
  - 单测:长文本走规则兜底→产出块均 ≤ ~1800(+overlap)。
  - 不影响语义路径;退 0。

## T4：语义可靠性(适度)

- **文件**：`embedding_splitter.go`(/split 重试)、`hybrid_splitter.go`(周期重探)。
- **改**：① `/split` 瞬时错误(超时/5xx/连接)重试 1 次,4xx 不重试;② `semanticAvailable` 改 TTL 周期重探(>30s 距上次探活则重探),线程安全(mutex/atomic)。
- **验收**：
  - 单测:/split 第一次失败第二次成功→最终成功(重试生效);4xx 不重试。
  - 单测:周期重探——语义从不可用变可用,下次 Split 超过 TTL 后重新走语义。
  - `go test -race`(并发安全)退 0。

## T5（S5 验证策略,Rule 10）

- **方式**：后端 TDD(每 task)+ dev 实跑 sanity。无 UI → 不做 Playwright。
- **理由**：纯后端入库逻辑;Go 单测覆盖核心 + 永久回归;dev 实跑确认真实链路(真 bge 语义 + 留痕落库 + 兜底路径)。
- **关键路径**：
  1. `task lint` + `go test ./...` + `go test -race ./internal/pkg/retrieval/...` 全绿。
  2. dev 部署后:传一份真实文档 → 查 `knowledge_document.split_strategy`=semantic + 无 WARN。
  3. **永不失败实证**：临时让 dev 语义失效(停 semantic_server 或指坏地址)→ 传文档 → 上传仍 COMPLETED、strategy=rule_fallback、有 WARN、块 ≤1800 → 恢复语义。
  4. dev 用留痕 SQL 统计现有/新增文档 strategy 分布 = **测出真实兜底率**(AC6,为后续是否重切存量提供数据)。
- **回归保护诚实声明**:Go 单测永久留存;dev 实跑一次性。涉及入库核心,reviewer 须确认"永不失败"不变式 + salesrag 检索零回归。

## Rule 11

起因=客户上报。T0 为分支第一个 commit(失败复现测试),`test(qa):` 前缀,reviewer grep commit log 验证。
