# S5 QA 报告：semantic-chunk-reliability

> S5 · 2026-06-17 · 验证策略=后端 TDD(每 task,T1/T2/T4 跑 -race)+ dev 实跑(无 UI → 无 Playwright)

## 1. 自动化测试（永久回归保护）

| 范围 | 命令 | 结果 |
|------|------|------|
| 改动包 + race | `go test -race ./internal/pkg/retrieval/ingest/` | ✅ 全绿 |
| 改动包 lint | `golangci-lint run ./internal/pkg/retrieval/... ./internal/pkg/model/...` | ✅ exit 0 |
| vet | `go vet ./internal/pkg/retrieval/... ./internal/pkg/model/...` | ✅ |
| 全仓 | `go test ./...` | ✅ 我的包全绿；仅 salesrag credits + agent student-skills 预存失败(base origin/develop 同样 FAIL,与本改动无关,已隔离核实) |

## 2. 关键路径覆盖（Go 单测）

- **T0 复现(Rule 11)**：`TestIngestionPipeline_PersistsSplitStrategy_OnFallback` — 兜底入库须 (a) COMPLETED+非空 (b) 留痕 rule_fallback。RED→GREEN,永久留存。
- **T1 留痕**：`TestNormalizeStrategy`(rule+rule_fallback→rule_fallback)/`TestSplitWithStrategy_*`(永不 err、no_split)。
- **T2 永不失败**：`TestIngestionPipeline_RealSplitter_NeverFailsOnSemanticDown` — 真实 splitter + 语义 down → COMPLETED + rule_fallback。
- **T3 好兜底**：`TestFallbackChunkSize_Reasonable` — 长文兜底 ≥2 块、每块 ≤~1800+overlap。
- **T4 可靠性**：`TestEmbeddingSplitter_RetryOnTransient`(5xx 重试)/`TestEmbeddingSplitter_NoRetryOn4xx`/`TestHybridSplitter_ReprobeAfterTTL`(崩溃恢复)——全 -race。

## 3. 不变式

- **永不失败**：`SplitWithStrategy` 恒返回 nil err + 非空 chunk(末级兜底整段成 1 块);pipeline split 路径不再 p.fail。
- **并发安全**：`semanticAvailable`/`lastProbeAt` 锁保护,探活在锁外短超时,`-race` 净。

## 4. dev 实跑 sanity（部署后执行）

0. 先手工跑 migration + `SHOW COLUMNS FROM knowledge_document LIKE 'split_%'` 确认 2 列在(防 AutoMigrate 静默跳过)。
1. 传一份真实文档 → `SELECT split_strategy FROM knowledge_document WHERE id=<new>` = `semantic` + 无 WARN。
2. 永不失败实证:临时停 dev semantic_server → 传文档 → 仍 COMPLETED + strategy=rule_fallback + WARN + 块 ≤1800。
3. 恢复 semantic_server → 再传 → strategy=semantic(周期重探生效)。
4. 真实兜底率(AC6):`SELECT split_strategy, COUNT(*) FROM knowledge_document WHERE split_strategy IS NOT NULL AND split_strategy != '' GROUP BY split_strategy`(对 NULL 与空串都鲁棒)。

## 5. 结论

后端逻辑 S5 通过(全绿 + race + lint)。dev 实跑 sanity 在 ndf-done + /deploy-dev 后执行。回归保护由 Go 单测永久承担。
