# 实施计划 Plan：semantic-chunk-reliability

> S3 · 2026-06-17 · 串行(多数 task 触及 ingest 重叠区,主 session 自实现 + 每 task 双 Sonnet reviewer)

## 依赖 / 顺序

```
T0 复现测试(RED) → T1 留痕(含加列+migration) → T2 永不失败加固 → T3 好兜底 → T4 语义可靠性
```
T1 是 keystone(复现转 GREEN)。T2/T3/T4 相对独立,但都在 ingest 区,串行实现。

## 统一决定(已纳入 reviewer P0/P1,所有 task 遵守)
- **标签归一**：`SplitWithStrategy` 把 `rule`+`rule_fallback` 统一成 `rule_fallback`,只产出 `semantic`/`rule_fallback`/`no_split` 三值。
- **接口可选 + 类型断言**:不改 `TextSplitter` 接口;新增 `StrategyAwareSplitter` 可选接口,pipeline 类型断言(照搬 `pipeline.go:251` 的 `UpdateColumns` 模式),断言失败降级 `Split()`。
- **永不失败硬保证**:`SplitWithStrategy` 永不因切块返回 err(规则切块器也报错时,最后兜底返回整段=1 chunk + rule_fallback)。
- **并发安全**:`semanticAvailable`/`lastProbeAt` 加锁;T1/T2/T4 均跑 `go test -race`。

## T0：复现测试(Rule 11,分支第一个 commit,先于任何实现)

- 客户上报"语义经常不工作"→ 第一个 commit 必须是失败复现测试,`test(qa):` 前缀。
- **位置**：`internal/pkg/retrieval/ingest/pipeline_strategy_test.go`。
- **具体 RED 机制(定死)**:fake splitter 模拟"语义不可用→兜底",经 pipeline(mock docStore+chunkStore)跑完整入库,断言 (a) `doc.SplitStrategy=="rule_fallback"`;(b) doc=COMPLETED 且 chunk 非空;(c) mock docStore 收到 strategy 写入。**修前 RED**——`SplitStrategy` 字段不存在 + pipeline 不持久化 strategy（编译+断言失败）。
- 修复后(T1/T2)转 GREEN,永久留存。reviewer grep commit log 验证 `test(qa):` 在最前。

## T1：留痕(可观测)

- **文件**：`splitter_adapter.go`(加 `SplitWithStrategy` + `StrategyAwareSplitter` 接口,归一标签)、`hybrid_splitter.go`(复用 `SplitWithDetails`)、`pipeline.go`(类型断言调用+持久化)、`model/knowledge_document.go`(加 2 列,进 AutoMigrate)、`migrations/<ts>_add_split_strategy.sql`(新,information_schema 守卫)、store 层(写入,优先复用现有 `UpdateColumns`)。
- **验收**：
  - 单测:`SplitWithStrategy` 返回归一 strategy;语义/规则错都不返回 err(永不失败)。
  - 单测:pipeline 经类型断言拿到 strategy+detail 并写进 doc;**写入失败只日志不阻断入库**。
  - struct 加字段进 AutoMigrate + 守卫式 migration 双管齐下(新装靠 AutoMigrate,存量靠 SQL)。
  - `go test ./internal/pkg/retrieval/... ./internal/numind/...` + **`go test -race ./internal/pkg/retrieval/...`** + `task lint` 退 0。

## T2：永不失败加固 + 回归测试

- **文件**：`pipeline.go`(审计 fail 点)、`hybrid_splitter.go`(规则切块器也报错时最后兜底返回整段 1 chunk)、`pipeline_strategy_test.go`(扩)。
- **验收**：
  - 单测:语义整段不可用(fake)→ pipeline 处理完 doc=COMPLETED、chunk 非空、strategy=rule_fallback、无 err。
  - 单测:连规则切块器都报错→仍返回整段 1 chunk(最后兜底),不 p.fail。
  - 审计范围**仅 splitting 路径**(pipeline.go:170-178);tagging fail(:211)/vector fail(:241)是既有硬错路径、本 feature 不改,reviewer 确认即可(S3-P2)。
  - `go test -race` 退 0。

## T3：兜底切得好

- **文件**：`splitter_adapter.go`(NewCompatibilitySplitter 的 RuleConfig)。
- **改**：MaxChunkSize 6000→1800,**MinChunkSize 1500→900**(给切分留窗口);保留 jieba+markdown。
- **验收**：
  - 单测:2000 字文本走规则兜底→产出 ≥2 块且每块 ≤ ~1800(+overlap)。
  - 不影响语义路径;退 0。

## T4：语义可靠性(适度)

> reviewer 升级:本 task 含 P0 修复(既有 `semanticAvailable` 无锁 race + `IsAvailable()` 复用 600s 超时卡死入库)。
- **文件**：`embedding_splitter.go`(/split 重试 + **独立短超时探活 client 3-5s**)、`hybrid_splitter.go`(并发安全 + check-on-call TTL 周期重探)。
- **改**：① `semanticAvailable`+`lastProbeAt` 全部读写加锁(含**既有**内联重连);② 探活用独立短超时 client,**绝不**复用 /split 的 600s client;③ check-on-call TTL(每次 Split 距上次探活 >30s 则短超时探一次,据此更新可用状态;**不起常驻 goroutine**);④ `/split` 瞬时错误(超时/5xx/连接)重试 1 次,4xx 不重试,返回 err 后由上层兜底(仍永不失败)。
- **验收**：
  - 单测:/split 第一次失败第二次成功→最终成功;4xx 不重试。
  - 单测:周期重探——语义从不可用变可用,超过 TTL 后下次 Split 重新走语义。
  - `go test -race ./internal/pkg/retrieval/...`(并发安全,**关键**)退 0。

## T5（S5 验证策略,Rule 10）

- **方式**：后端 TDD(每 task)+ dev 实跑 sanity。无 UI → 不做 Playwright。
- **理由**：纯后端入库逻辑;Go 单测覆盖核心 + 永久回归;dev 实跑确认真实链路(真 bge 语义 + 留痕落库 + 兜底路径)。
- **关键路径**：
  1. `task lint` + `go test ./...` + `go test -race ./internal/pkg/retrieval/...` 全绿。
  2. dev 部署后**先手工跑 migration** + `SHOW COLUMNS FROM knowledge_document LIKE 'split_%'` 确认 2 列在(防 AutoMigrate 静默跳过),再往下。
  3. 传一份真实文档 → 查 `split_strategy`=semantic + 无 WARN。
  4. **永不失败实证**：临时停 dev semantic_server(或指坏地址)→ 传文档 → 仍 COMPLETED、strategy=rule_fallback、有 WARN、块 ≤1800。
  5. **恢复并复验(显式子步,防遗忘)**:恢复 semantic_server → 再传文档 → strategy=semantic(证明周期重探生效)。
  6. **测真实兜底率**(AC6):`SELECT split_strategy, COUNT(*) ... WHERE split_strategy IS NOT NULL GROUP BY split_strategy`(排除历史 NULL)。
- **回归保护诚实声明**:Go 单测永久留存;dev 实跑一次性。涉及入库核心,reviewer 须确认"永不失败"不变式 + salesrag 检索零回归。

## Rule 11

起因=客户上报。T0 为分支第一个 commit(失败复现测试),`test(qa):` 前缀,reviewer grep commit log 验证。
