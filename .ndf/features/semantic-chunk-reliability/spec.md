# 技术设计 Spec：semantic-chunk-reliability

> S2 · 2026-06-17 · 权威设计

## 0. 北极星 + 不变式

**不变式（任何改动不得破坏）**：知识库入库**永不因切块策略问题而失败**——语义不可用/出错时优雅兜底,文档仍 COMPLETED、chunk 非空。

优先级：稳定语义 > 失败也成功(好兜底) > 全程留痕。

## 1. 现状（实证）

- `pipeline.go:170` `chunks, err := p.splitter.Split(markdown)`——用 `Split()`,丢弃了 strategy。
- `hybrid_splitter.go` `SplitWithDetails()` 已返回 `details["strategy"]`(`no_split`/`semantic`/`rule_fallback`/`rule`)+ `semantic_error`,但没人调。
- `CompatibilitySplitter`(`splitter_adapter.go`)→`SplitterAdapter`→`HybridSplitter`,只暴露 `Split()`。
- `knowledge_document`(`model/knowledge_document.go`,GORM)无 strategy 字段。
- 规则兜底 `MaxChunkSize 6000`(`splitter_adapter.go:58`)偏大。
- `EmbeddingSplitter.Split`(`embedding_splitter.go`)调 `/split` 失败即返回 err（上层兜底）,无重试。`HybridSplitter` 只在 `!semanticAvailable` 时重探。

## 2. 改动设计（4 task）

### T0（Rule 11 复现测试,分支第一个 commit）
起因是客户上报"语义经常不工作"。第一个 commit = 失败复现测试：断言**入库后能拿到切块 strategy 且语义不可用时上传仍成功并标记 rule_fallback**——修前没有 strategy 留痕机制 → 测试 FAIL(编译失败/字段不存在/拿不到 strategy)。`test(qa):` 前缀。位置 `internal/pkg/retrieval/ingest/pipeline_strategy_test.go`。

### T1：留痕(可观测,keystone)
- **暴露 strategy**:`SplitterAdapter` 加 `SplitWithStrategy(text) ([]SplitChunk, strategy string, detail string, err error)`(内部调 `hybrid.SplitWithDetails`,取 `details["strategy"]` + reason)。`CompatibilitySplitter` 同样透传。
- **pipeline 捕获**:`pipeline.go` 改调 `SplitWithStrategy`(或新接口方法),拿到 strategy+detail。`TextSplitter` 接口加可选方法或扩展(保持 `Split` 兼容)。
- **持久化**:`knowledge_document` 加列 `split_strategy varchar(20)` + `split_detail varchar(512)`。migration `migrations/YYYYMMDD_add_split_strategy.sql`(ADD COLUMN IF NOT EXISTS 守卫,见 20260515 风格)。pipeline 在切块后 `docStore` 写入(扩 store 方法或在 status 更新时带上)。
- **日志**:strategy=rule_fallback 时 `log.Warnw("chunk fallback to rule-based", "doc_id", ..., "reason", detail)`。
- **永不失败**:strategy 写入失败**只记日志、不阻断入库**(留痕是辅助,不能反过来害了主流程)。

### T2：永不失败加固 + 回归测试
- 审计 `pipeline` 处理链:确认切块层不会因语义问题 fail(`p.fail` 只该用于真正无法产出 chunk 的硬错,如解析失败/空文档)。
- 回归测试:语义不可用(mock EmbeddingSplitter 不可用 / `/split` 报错)→ `HybridSplitter.Split` 仍返回非空 chunk、无 err → pipeline 走完 COMPLETED。断言不变式。

### T3：兜底切得好
- `splitter_adapter.go:58` 规则兜底 `MaxChunkSize 6000`→ **1800**(600 汉字\*3),`MinChunkSize 1500`→ 保持或 900;`OverlapSize 300`→ 保持。贴近语义档(500-2000),保留 `EnableJieba`+`ProtectMarkdown`。
- 仅影响**新入库**走兜底时的块大小;不动存量、不重嵌。

### T4：语义可靠性(适度)
- **重试**:`EmbeddingSplitter.Split` 调 `/split` 遇瞬时错误(超时/5xx/连接失败)先重试 1 次(短延迟)再返回 err。永久错误(4xx)不重试。
- **周期重探**:`HybridSplitter` 把"仅 `!semanticAvailable` 时重探"改为**带 TTL 的周期重探**(如每次 Split 若距上次探活 >30s 则重探一次),语义崩溃恢复后无需重启即可重新启用。线程安全(单例并发,加锁或 atomic)。
- 不动 entrypoint 的启动等待逻辑(那是部署层,本次不碰)。

## 3. 持久化字段

`knowledge_document`:
- `split_strategy varchar(20)`:`semantic` / `rule_fallback` / `no_split`(短文不切)/ 空(历史行)。
- `split_detail varchar(512)`:原因或比例(如 `semantic_error: timeout` / `semantic_unavailable`）。
GORM model 加字段 + migration(AutoMigrate 可能不覆盖,部署时按 information_schema 守卫手工跑,见 thinking-activation 经验)。

## 4. 验收映射

| AC | task | 验证 |
|----|------|------|
| AC1 留痕 | T1 | 单测 + dev 实跑查 split_strategy 落库 + WARN 日志 |
| AC2 永不失败 | T2 | 单测:语义不可用→仍 COMPLETED+非空+rule_fallback |
| AC3 好兜底 | T3 | 单测:兜底块 ≤~1800;dev 兜底块大小合理 |
| AC4 可靠性 | T4 | 单测:/split 瞬时失败重试;周期重探 TTL |
| AC5 语义优先 | (现状) | dev 实跑:语义可用→strategy=semantic |
| AC6 可量测 | T1 | dev SQL 统计 strategy 分布 |

## 5. 验证策略(S5,Rule 10)

后端单测为主(每 task)+ **dev 实跑**:① 传一份真实文档→查 `knowledge_document.split_strategy`=semantic + 日志;② 人为让语义失败(临时停 semantic_server 或指向坏地址)→ 传文档→上传仍成功、strategy=rule_fallback、有 WARN、块大小合理→恢复。无 UI → 不做 Playwright。回归保护靠 Go 单测永久留存。

## 6. 不做

重切重嵌存量(等 AC6 数据);语义模型升级;评估集;前端;entrypoint 启动逻辑。
