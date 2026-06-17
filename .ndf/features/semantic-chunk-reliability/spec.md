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

## 2. 改动设计（5 task）— 已纳入 S2/S3 reviewer 的 P0/P1 修正

### 跨 task 的统一决定（reviewer 修正）
- **strategy 标签归一**（S2-F1/F9, S3）：`SplitWithDetails` 现返回 4 值(`no_split`/`semantic`/`rule`/`rule_fallback`)。其中 `rule`(语义从未可用)与 `rule_fallback`(试了语义失败)对用户/统计无区别 → **`SplitWithStrategy` 一律归一为 `rule_fallback`**(只保留 `semantic`/`rule_fallback`/`no_split` 三值)。AC1/AC2/AC6 都用归一后的值。
- **接口扩展走可选接口 + 类型断言**（S2-F4, S3）：**不改** `TextSplitter` 接口(否则所有 mock 编译失败)。新增可选接口 `StrategyAwareSplitter { SplitWithStrategy(text)(chunks []SplitChunk, strategy, detail string, err error) }`,`CompatibilitySplitter` 实现它(转发 `SplitterAdapter`→`hybrid.SplitWithDetails`)。pipeline **类型断言**(照搬 `pipeline.go:251` 对 `UpdateColumns` 的现有模式),断言失败则降级用旧 `Split()`。
- **永不失败硬保证**（S2-F3, S3）：`SplitWithStrategy` **永不因切块返回 err**——任何内部错误(语义 err / 规则切块器 err)最终都至少返回**整段文本作为 1 个 chunk** + strategy=`rule_fallback`,err=nil。这样 pipeline 的 split 路径不可能触发 `p.fail`。

### T0（Rule 11 复现测试,分支第一个 commit,`test(qa):` 前缀）
- **位置**：`internal/pkg/retrieval/ingest/pipeline_strategy_test.go`。
- **具体 RED 机制(S3-P0,定死,非纯编译错)**：测试构造一个 fake splitter,模拟"语义不可用→走兜底",通过 pipeline(用 mock docStore + mock chunkStore)跑完整入库,断言三件事:(a) `doc.SplitStrategy == "rule_fallback"`(留痕);(b) doc 最终 `COMPLETED`、chunk 非空(永不失败);(c) mock docStore 收到了 strategy 写入。修前 RED——`SplitStrategy` 字段不存在 + pipeline 不持久化 strategy → 编译/断言失败。修后(T1+T2)GREEN,永久留存。
- 注:断言用归一后的 `rule_fallback`(见上"标签归一")。

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
- `splitter_adapter.go:58` 规则兜底 `MaxChunkSize 6000`→ **1800**(600 汉字\*3),`MinChunkSize 1500`→ **900**(300 汉字\*3,给切分留足窗口,reviewer S3-P2:1500/1800 仅 300 窗口几乎不切);`OverlapSize 300`→ 保持。贴近语义档,保留 `EnableJieba`+`ProtectMarkdown`。
- 单测断言:2000 字文本走规则兜底产出 ≥2 块且每块 ≤~1800+overlap。
- 仅影响**新入库**走兜底时的块大小;不动存量、不重嵌。顺带:`NewCompatibilitySplitter` 的 `cfg SplitterConfig` 参数是死参(未透传),保持现状不动,加注释说明避免误解(S2-F8)。

### T4：语义可靠性 + 并发安全(reviewer 升级为 P0 关键 task)
> **S2-F2(P0)**:`semanticAvailable` 在单例上无锁读写 = data race;且 `IsAvailable()` 复用 600s 超时的 HTTP client,语义服务慢响应会**卡死入库 goroutine 最多 10 分钟**→ 间接威胁"永不失败"。本 task 必须一并修既有的不安全重连,不只加新探活。
- **并发安全**:`HybridSplitter.semanticAvailable`(+ 新增 `lastProbeAt`)的所有读写用 `sync.RWMutex` 或 `atomic` 保护。`Split`/`SplitWithDetails` 里现有的内联重连一并纳入保护。`go test -race` 验证。
- **短超时探活**:`IsAvailable()`/周期探活**必须用独立的短超时 client(3-5s)**,不得复用 `/split` 的 600s client(否则慢响应卡死入库)。`embedding_splitter.go` 加一个短超时探活路径。
- **周期重探(check-on-call + TTL,不起后台 goroutine)**:每次 `Split`/`SplitWithStrategy` 若距上次探活 >TTL(如 30s)则探一次(短超时),据此更新 `semanticAvailable`。语义崩溃恢复后无需重启即可重新启用。**不**起常驻 goroutine(避免生命周期/关停复杂度,S2-F7)。
- **重试**:`EmbeddingSplitter.Split` 调 `/split` 遇瞬时错误(超时/5xx/连接失败)先重试 1 次(短延迟)再返回 err;永久错误(4xx)不重试。返回 err 后由上层(HybridSplitter)兜底——仍满足永不失败。
- 不动 entrypoint 启动等待逻辑(部署层)。

## 3. 持久化字段 + migration（reviewer S2-F5/S3 修正）

`knowledge_document` 加 2 列(归一后只用 3 个 strategy 值):
- `split_strategy varchar(20)`:`semantic` / `rule_fallback` / `no_split`(短文不切)/ 空(历史行,统计时 `WHERE split_strategy IS NOT NULL` 排除)。
- `split_detail varchar(512)`:原因(如 `semantic_error: timeout` / `semantic_unavailable`)。

**双管齐下**(确保新装 + 存量都建列):
1. 加字段到 Go struct `model.KnowledgeDocument`（它在 `helper.go` 的 `db.AutoMigrate(...)` 批次里 → AutoMigrate 覆盖**新装/dev 重启**的简单 ADD COLUMN）。
2. 同时提供 `migrations/<ts>_add_split_strategy.sql`,用 information_schema 守卫(`SET @col_exists / PREPARE / EXECUTE`,照搬 `20260611_100000_add_ai_service_thinking_style.sql`)——给**存量/prod** 用。
3. **S5/部署时先手工跑 migration + `SHOW COLUMNS FROM knowledge_document LIKE 'split_%'` 确认 2 列存在**,再做其它验证(防 AutoMigrate 静默跳过)。
- strategy 写入失败**只记日志、不阻断入库**(留痕是辅助,绝不反害主流程)。

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

后端单测为主(每 task,T1/T2/T4 都跑 `go test -race`)+ **dev 实跑**:
0. **先手工跑 migration** + `SHOW COLUMNS FROM knowledge_document LIKE 'split_%'` 确认 2 列在(防 AutoMigrate 静默跳过),再往下。
1. 传一份真实文档 → 查 `knowledge_document.split_strategy`=semantic + 无 WARN。
2. **永不失败实证**:临时停 dev semantic_server(或指坏地址)→ 传文档 → 上传仍 COMPLETED、strategy=rule_fallback、有 WARN、块 ≤1800。
3. **恢复并复验(显式子步,防遗忘)**:恢复 semantic_server → 再传一份文档 → 确认 strategy=semantic(证明周期重探生效)。
4. **测真实兜底率**(AC6):`SELECT split_strategy, COUNT(*) ... WHERE split_strategy IS NOT NULL GROUP BY split_strategy`(排除历史 NULL 行)。
无 UI → 不做 Playwright。回归保护靠 Go 单测永久留存。

## 6. 不做

重切重嵌存量(等 AC6 数据);语义模型升级;评估集;前端;entrypoint 启动逻辑。
