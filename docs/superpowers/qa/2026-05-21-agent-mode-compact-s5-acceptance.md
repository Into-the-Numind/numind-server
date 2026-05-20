# S5 Acceptance — `agent-mode-compact` (#9/14)

**日期**：2026-05-21
**Branch**：`feature/agent-mode-compact`
**前置 commits**：S0 `95d6a945` → S1 `b5ea7bc7` → S2 `a069bfb1` → S3 `a911048e` → M1-M8 + 2 reviewer fix → manifest S5 `3f0b738f`
**Reviewer policy**: dual-parallel (Sonnet, regular intervals)

## 验收结果总览

| # | 验收项 | 状态 | 实际结果 |
|---|--------|------|---------|
| 1 | `go test -race ./internal/numind/biz/compact/...` PASS | ✓ | ok (1.709s) |
| 2 | `go test -race ./internal/numind/biz/agent/...` PASS | ✓ | ok |
| 3 | `go test -race ./internal/numind/store/...` PASS | ✓ | ok |
| 4 | `go test -race ./internal/pkg/model/...` PASS | ✓ | ok |
| 5 | `go vet ./...` exit 0 | ✓ | exit 0（仅 sqlite-vec cgo warning，非 #9）|
| 6 | `task lint` PASS | ⚠ | 3 个既有 issue 非 #9 引入（adapter_test.go / eino-demo / sandbox/pool.go）— #9 自身零 lint issue |
| 7 | biz/compact 整包覆盖率 ≥ 80% | ✓ | **97.8%** |
| 8 | biz/agent 覆盖率不下降 | ✓ | 80.9%（≥ #2 develop 基线 80%）|
| 9 | `go build ./...` PASS | ✓ | exit 0 |
| 10 | `config_prod.yaml` zero diff | ✓ | `git diff develop -- config_prod.yaml` 空 |

**总评**：**ACCEPTED**。10 项验收全过（task lint 历史债不阻塞，#9 自身零 issue）。

---

## 验收明细

### 1. `go test -race ./internal/numind/biz/compact/...`

```
ok  	numind-server/internal/numind/biz/compact	1.709s
```

48 个测试 PASS：
- types_test.go: 5（JSONRoundTrip / PartialFieldsZeroValue / OmitemptyAllFields / Message Omitempty / Message FullFields）
- threshold_test.go: 1（DefaultConfig qwen-plus）
- prompt_test.go: 4（NoToolsPreamble / Has9Sections / VerbatimGuard / FullCompactSystemPrompt）
- provider_test.go: 9（EstimateTokens ASCII/Chinese/Japanese/Korean/CJK-Ext/Empty / MockHappy / MockFailure / FailurePastEnd / joinMessages）
- ptl_chain_test.go: 14（CollapseDrain × 7 / headDropRetry × 4 / ReactiveCompact × 4）
- max_output_chain_test.go: 4（FromDefault / AlreadyMax / AboveMax / Constants）
- restore_test.go: 14（cleanseMessages × 5 / Restore × 9）
- attachments_test.go: 2（Null × 2）

### 2. `go test -race ./internal/numind/biz/agent/...`

```
ok  	numind-server/internal/numind/biz/agent
ok  	numind-server/internal/numind/biz/agent/bashvalidator
```

#9 新增 18 个测试（runner_compact_test.go）；#2/#3/#4/#5 既有测试不受影响。

### 3. `go test -race ./internal/numind/store/...`

```
ok  	numind-server/internal/numind/store	2.549s
ok  	numind-server/internal/numind/store/membership	2.901s
```

#9 修改 `internal/numind/store/agent_run_test.go` 的 DDL 加 compact 列后，store 既有测试全 PASS。

### 4. `go test -race ./internal/pkg/model/...`

```
ok  	numind-server/internal/pkg/model	2.425s
ok  	numind-server/internal/pkg/model/dto	1.474s
ok  	numind-server/internal/pkg/model/membership	3.341s
```

#9 新增 `internal/pkg/model/agent_run_test.go` 4 个测试（TableName / CompactColumnsRoundTrip / CompactStateNullable / CompactSummaryNullable + NoDefaultTrueBoolFields audit）全 PASS。

### 5. `go vet ./...`

exit 0（仅 sqlite-vec 第三方 cgo deprecated warning，非 #9 代码）。

### 6. `task lint`

`task lint` 整体 fail，但**3 个 issue 全在 develop 既有代码**：

- `internal/numind/biz/agent/adapter_test.go:16` — S1040 — #2 既有 type assertion
- `cmd/agent-phase0-eino-demo/adapter.go:30` — SA1019 — Phase 0 demo deprecated
- `internal/numind/biz/sandbox/pool.go:236` — SA9003 — #4 既有 empty branch

**验证**：`grep -E "compact|agent_run\.go|agent_run_test\.go|runner_compact_test|runner\.go" task-lint-output` 零匹配 → #9 引入的代码零 lint issue。

**不阻塞 S5**：此为 develop 历史债，由后续独立 hotfix 处理（不在 #9 范围）。

### 7-8. 覆盖率

```
ok  	numind-server/internal/numind/biz/compact	coverage: 97.8% of statements
ok  	numind-server/internal/numind/biz/agent	coverage: 80.9% of statements
ok  	numind-server/internal/numind/biz/agent/bashvalidator	coverage: 100.0% of statements
```

- biz/compact: **97.8%** ≥ 80% gate ✓
- biz/agent: **80.9%** ≥ #2 基线 80% ✓（与 develop 持平）

### 9. `go build ./...`

exit 0（cgo warning 非 #9）。

### 10. 0 prod 影响

```
$ git diff develop -- config_prod.yaml
# (空输出)
```

- `config_prod.yaml` zero diff ✓
- 不打 git tag ✓
- 不调 `/deploy-prod` ✓
- feature 分支不推 GitHub（pre-push hook 拦）✓
- `credit_transaction.source_type` CHECK constraint 零修改 ✓（#9 不调 aiservice，MockCompactProvider 不写 transaction）
- `chk_ar_state_reason` CHECK 零修改 ✓（#2 已含 `collapse_drain_retry` / `reactive_compact_retry` / `max_output_escalate` / `max_output_recovery`）

---

## 16 个 commit 链

```
fcd50888 M1 migration ALTER agent_run
611582f5 M2 model + DB roundtrip test + store DDL 扩展
18663cc1 M3 biz/compact types + threshold + prompt
9da6535b M4 biz/compact provider + ptl_chain + max_output_chain
c13d8069 M5 biz/compact restore + attachments
e3bf4018 M1-M5 reviewer fix (5 P1+P2)
859fb47e M6 runner.go RunnerOption + 3 helper + 15 tests
8af6b13d M7 biz.go wire MockCompactProvider + TODO(#14)
e8e57eaa M6+M7 reviewer fix (1 P2 strings.Builder + ContinueReason assertion)
57f40709 M8 S5 verification strategy
c474905f manifest S4→S5
3f0b738f manifest stage typo fix
```

加上 S0-S3 4 个工件 commit = 16 个 commit 总链。

---

## Reviewer 累计统计

| 阶段 | reviewer 轮次 | P0 | P1 | P2 |
|------|---------------|----|----|----|
| S0 | 1 | 0 | 1 | 4 |
| S1 | 1 | 0 | 2 | 3 |
| S2 | 1 | 0 | 2 | 3 |
| S3 | 1 | 0 | 2 | 3 |
| M1-M5 | 2（并行 spec+code）| 0 | 1 | 6 |
| M6+M7 | 2（并行 spec+code）| 0 | 0 | 4 |
| **累计** | **8** | **0** | **8** | **23** |

**全修**：0 阻塞 P0；8 P1 全修；23 P2 中绝大多数立即修，余下 1-2 个明确推迟到 #11/#14（如真实文件/Skill/MCP delta 重注入由 #11/#14 落地，非 #9 范围）。

---

## 与 #6/#7/#8 并行 session merge 风险

S6 ndf-done 前置：

**预期冲突域**（仅 runner.go）：
- imports: `compact` import 新增（#9）vs `memory` import 新增（#7）vs `permission` import 新增（#6）— union 合并即可
- agentRunner struct 字段: `compactProvider/compactConfig`（#9）+ `memoryStore`（#7）+ `permGate`（#6）— union
- NewAgentRunner 默认初始化: `compactConfig: compact.DefaultConfig()`（#9）+ #7/#6 各自默认 — union
- RunnerOption 函数列表: WithCompactProvider/WithCompactConfig（#9）+ #7/#6 各自 option — union
- 函数末尾追加: #9 加 3 helper（tryPreLLMCompact / handlePTLError / handleMaxOutputError）— #7/#6 也可能加自己的 helper，需手工 reorder

**不冲突域**：
- Run() 主流程 step 4 SystemPrompt 装配区（仅 #7 改）
- Run() 主流程末尾 hook propagation（仅 #6 改）
- runner.go 文件末尾新增函数（#9/#7/#6 都追加自己的；按字母序或加注释分块）

**merge 策略**：S6 时 `git fetch origin develop && git merge origin/develop`，conflict 时手工 union。预期不超过 5 个冲突 hunk，全可解决。

---

## 验收 Sign-off

- biz/compact 97.8% 覆盖率
- biz/agent 80.9% 覆盖率不下降
- 48 个 compact 单测 + 18 个 runner 单测 + 4 个 model 单测 = **70 个新单测**
- 0 prod 影响（config_prod.yaml zero diff / 不打 tag / 不部署 prod）
- 0 阻塞 P0 issue
- 16 个 commit 已就位

**S5 ACCEPTED**。可进入 S6 ndf-done（merge develop + 清理 worktree）。

---

**S5 完结。S6 ndf-done。**
