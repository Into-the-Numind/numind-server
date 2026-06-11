# S5 QA 报告：thinking-activation-style

date: 2026-06-11 | 验证环境：本地（feature 分支，未 merge）| 策略：后端单测（T4）

## 验证矩阵（AC ↔ 测试）
| AC | 验证 | 结果 |
|----|------|------|
| AC1 enable_thinking_kwarg → chat_template_kwargs | dmxapi_thinking_test case 9 (agnes-2.0-flash) | ✅ PASS |
| AC2 ''/reasoning_effort → reasoning_effort:medium（零回归）| case 8(legacy '') + case 10(显式) + 原 8 case 全绿 | ✅ PASS |
| AC3 none → 不发任何字段 | case 11 | ✅ PASS |
| AC4 thinking_only → intrinsic 不变 | case 5/7 | ✅ PASS |
| AC5 gate off（Thinking=false / supports=false）→ 不发 | case 12/13 | ✅ PASS |
| AC7 Claude base+Thinking 仍强制 temp=1 | case 1/2 | ✅ PASS |
| AC6 agnes dev 数据修正 | 延后到 S6 dev 实跑（migration UPDATE）| ⏳ S6 |
| registry 读取 thinking_style | store_test ReadsThinkingStyle（4 枚举值 × 2 resolver）| ✅ PASS |

## 命令结果
- `go vet ./...`：exit 0（仅 sqlite-vec cgo deprecation 警告，pre-existing，与本改动无关）
- `golangci-lint run ./internal/pkg/aiservice/... ./internal/pkg/model/`：exit 0
- `go test ./...`：全绿，无 FAIL（含 aiservice 全子包）
- `go test -race ./internal/pkg/aiservice/...`：全绿（ld malformed LC_DYSYMTAB 警告为 macOS 链接器噪声，pre-existing）

## 可观测性（ai-service.md 合规）
本改动不新增 LLM 调用入口，仅改请求体构造 + TraceMetadata.ResolvedReasoningEffort 新增 sentinel（"enable_thinking_kwarg"/"none"），使思考激活方式在 Langfuse 可审计。trace 拓扑不变，无需新增 generation/span。

## 回归保护诚实声明
- 持久化回归保护：13 case 的 wire-body 矩阵 + registry store 测试，覆盖每条 thinking_style 分支，永久留库。
- 一次性验证：S6 dev 上用 agnes-2.0-flash 实跑确认返回 reasoning_content（agnes 未来改动需手动重跑——非支付/权限高风险，单测覆盖足够）。

## 不在 S5（移交 S6）
- agnes-2.0-flash dev 端到端实跑（需 code 上 dev + AutoMigrate 建列 + 手工跑 migration 的 agnes 数据修正 UPDATE）。
- **S6 前置检查**（防 migration-gap）：`SELECT thinking_style FROM ai_service WHERE model_key='agnes-2.0-flash'` 须返回 `enable_thinking_kwarg`。
