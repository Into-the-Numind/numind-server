# QA Report — tool-soft-error-sweep (S5)

Date: 2026-06-12 · 验证方式：仅后端 Go TDD（plan T4 策略，S3 gate reviewer 背书）

## 复现测试（Rule 11，永久回归保护）

`internal/numind/biz/agent/tool_soft_error_sweep_test.go`

- T0 commit 4255d39c 时：17/17 RED（失败指纹与线上事故一字不差：run 136 `cannot unmarshal bool into ... webFetchInput.prompt`、run 137 `prompt is required`）
- T3 完成后：17/17 GREEN + RecoverableRuntimeErrors GREEN

## 验收结果

| 检查 | 结果 |
|---|---|
| AC1 复现测试 RED→GREEN | ✅（上表） |
| AC2 修复清单全落实 | ✅ spec §2 #1-#11 全部，3 轮 spec-compliance reviewer 逐行核对 |
| AC3 `go test ./...` 全仓 | ✅ exit 0，0 FAIL（仅 cgo sqlite 弃用警告=环境噪声） |
| AC3 race | ✅ `go test -race -count=1 ./internal/numind/biz/agent/` ok 17.4s 无 DATA RACE |
| AC3 lint | ✅ `golangci-lint run ./...` 全仓 0 issue |
| AC4 已加固工具零回归 | ✅ 全包测试 GREEN；I3 零 diff 经 reviewer 确认 |

## Review 轨迹（Rule 6 双 Sonnet 并行 ×3 task + S3 gate + T3 复核）

- S3 gate：PASS（1 P1 spec 措辞 + 3 P2，全部现场修）
- T1：PASS/PASS（3 P2 现场修：工具名前缀、空 title、helper 延至 T2）
- T2：PASS/PASS（1 P2 现场修：create_html 双前缀）
- T3：PASS/FAIL → 3 P1 现场修（ops 可观测性 Warnw ×6、LLM payload 内部 issue 引用剥离）→ 复核 PASS（残留 P2 Description 引用也已清）

## 已知边界（诚实声明）

- COS 上传失败、向量库故障等运行期错误现在对 LLM 可见且不杀 run；ops 可见性靠 `log.Warnw`（kb_search/memory ×6 处）+ COS uploader 内部既有 `log.Errorw`
- 端到端"Eino 收 nil error 不杀 run"契约由既有加固工具的线上行为（run 143/145 自愈）+ 单元测试共同背书；dev 部署后做正常路径冒烟，不强造模型烂参数
