# 飞书技能读取容错与平台分页 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 保留 LLM 自主选择飞书技能和业务命令，同时由平台隐藏 cursor、纠正明确的 reference 字段放错、有界读完整技能说明，并正确区分可恢复输入与真实失败。

**Architecture:** `feishu.SkillReader` 继续作为五技能和声明 reference 的安全 SOT，只增加一个先辨认真实签名 cursor、再纠正官方 Markdown reference 形状的兼容步骤。模型工具只展示 `skill/reference`，内部最多续读两页并对完整 64 KiB JSON 信封 fail closed；后端继续解码旧 cursor 以支持滚动部署。

**Tech Stack:** Go 1.24、Eino tool adapter、固定 lark-cli 1.0.68、testify、NDF v3 worktree。

---

## 文件结构

- `internal/numind/biz/feishu/skill_reader.go`：可信 cursor/reference 识别、当前技能声明集解析和单页读取。
- `internal/numind/biz/feishu/skill_reader_test.go`：真实固定 CLI harness 下的客户复现和安全边界。
- `internal/numind/biz/agent/tool_lark_skill_read.go`：模型协议、旧输入兼容、内部分页、错误分类和最终 JSON。
- `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`：模型工具 schema、分页、错误信封和泄漏回归。
- `internal/numind/biz/agent/runner_v2_artifact.go`：64 KiB 原子信封第二道防线和共享常量。
- `internal/numind/biz/agent/runner_v2_artifact_test.go`：可信工具 inline/超限与伪造同名工具 artifact 路径。

### Task 1: 用客户真实调用固化失败回归

**Files:**
- Modify: `internal/numind/biz/feishu/skill_reader_test.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`

- [ ] **Step 1: 在 SkillReader harness 添加 Dev run 227 两个 RED**

增加表驱动测试。每个 case 写入当前 skill 主页和声明 reference，然后把 reference 放进 `Cursor`：

```go
func TestSkillReader_RepairsDeclaredMarkdownReferencePlacedInCursor(t *testing.T) {
    tests := []struct{ skill, reference string }{
        {"lark-drive", "references/lark-drive-search.md"},
        {"lark-doc", "references/lark-doc-fetch.md"},
    }
    for _, tt := range tests {
        h := newSkillReaderHarness(t, skillReaderOptions{})
        h.writeResource(tt.skill, "SKILL.md", "[Read]("+tt.reference+")", true)
        h.writeResource(tt.skill, tt.reference, "controlled reference", false)
        page, err := h.reader.Read(h.context(), SkillReadRequest{
            AgentRunID: 227, Skill: tt.skill, Cursor: tt.reference,
        })
        require.NoError(t, err)
        require.Equal(t, tt.reference, page.Path)
        require.Equal(t, "controlled reference", page.Content)
    }
}
```

- [ ] **Step 2: 在 Agent tool 添加 schema/错误语义 RED**

增加测试证明新模型 schema/output 不含 cursor，旧 JSON 字段仍被 strict decoder 接受，并且 executor 的 `ErrSkillReadInvalid` 产生：

```json
{"code":"invalid_skill_input","recoverable":true,"retryable":false}
```

同时断言 `ErrSkillReadFailed` 仍为 `skill_read_unavailable/recoverable:false`。

- [ ] **Step 3: 运行 RED 并保存失败证据**

Run:

```bash
go test ./internal/numind/biz/feishu -run 'TestSkillReader_RepairsDeclaredMarkdownReferencePlacedInCursor' -count=1
go test ./internal/numind/biz/agent -run 'TestLark.*SkillRead.*(Cursor|Recoverable)' -count=1
```

Expected: SkillReader 返回 `feishu skill request rejected`；schema 仍包含 cursor，invalid 仍错误映射为不可恢复 skill read failure。

- [ ] **Step 4: 提交第一个代码 commit（必须保持 RED）**

```bash
git add internal/numind/biz/feishu/skill_reader_test.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go
git commit -m "test(qa): reproduce Lark skill reference cursor confusion"
```

### Task 2: 在 SkillReader 内安全纠正字段，不改变白名单

**Files:**
- Modify: `internal/numind/biz/feishu/skill_reader.go`
- Modify: `internal/numind/biz/feishu/skill_reader_test.go`

- [ ] **Step 1: 增加窄兼容 helper**

在 `Read` 校验 reference 前调用私有 helper。逻辑必须先尝试真实 cursor 解码，再仅接受官方 Markdown reference 形状：

```go
func (r *SkillReader) normalizeLegacyReferenceCursor(request SkillReadRequest) SkillReadRequest {
    if request.Reference != "" || request.Cursor == "" {
        return request
    }
    if _, err := r.decodeToken(request.Cursor, skillCursorKind); err == nil {
        return request
    }
    if validSkillReferenceInput(request.Cursor) && strings.HasSuffix(request.Cursor, ".md") {
        request.Reference, request.Cursor = request.Cursor, ""
    }
    return request
}
```

该 helper 不决定可读性；移动后仍必须通过 `resolveSkillReference` 的当前技能声明集唯一解析。

- [ ] **Step 2: 补全 fail-closed 组合测试**

测试 canonical/basename 成功；合法签名 cursor 保持分页；reference+cursor 同时存在不交换；undeclared、ambiguous、cross-skill、`../`、绝对路径、反斜杠、NUL、Unicode、过长、篡改、错 run/skill 和过期 cursor 均拒绝。非法 reference 最多只读固定当前主页，不执行目标 reference CLI。

- [ ] **Step 3: 跑 focused tests**

```bash
go test ./internal/numind/biz/feishu -run 'TestSkillReader_(RepairsDeclaredMarkdownReferencePlacedInCursor|Reference|Pagination|Cursor|Invalid)' -count=1
```

Expected: PASS。

- [ ] **Step 4: 提交安全纠错**

```bash
git add internal/numind/biz/feishu/skill_reader.go internal/numind/biz/feishu/skill_reader_test.go
git commit -m "fix(feishu): normalize misplaced skill references safely"
```

### Task 3: 隐藏 cursor、平台有界分页并修正错误语义

**Files:**
- Modify: `internal/numind/biz/agent/tool_lark_skill_read.go`
- Modify: `internal/numind/biz/agent/tool_lark_personal_workspace_test.go`

- [ ] **Step 1: 收紧模型可见协议并保留 wire 兼容**

`InputSchema` 删除 cursor property，只保留 `skill/reference`；`decodeStrictLarkToolObject` 仍允许 `"cursor"`。`larkSkillReadOutput` 删除 `Cursor`。工具 Description 改为“读取完整受控说明，分页由平台处理”。invalid 固定文案只提示使用 skill/reference。

- [ ] **Step 2: 实现最多两页的内部聚合**

用常量 `larkSkillReadMaxPages = 2`。第一页后若有 cursor，记录 cursor 防重复并构造第二次 `SkillReadRequest`。如果原 request reference 为空但第一页 path 不是 `SKILL.md`，将经过底层验证的 path 作为第二页 canonical reference。每页验证相同 skill、path 和 References，拼接 content 后 marshal 完整输出并检查：

```go
if len(encoded) > larkSkillReadAtomicOutputLimit {
    return larkWorkspaceSoftError(larkWorkspaceErrorSkillRead)
}
```

第二页仍有 cursor、cursor 重复、`Skill`/`Path`/`References` 任一不一致或第二页任何错误都返回不可恢复固定错误，不返回第一页正文。

- [ ] **Step 3: 正确分类首次输入错误**

仅首次 `Read` 的 `errors.Is(err, feishu.ErrSkillReadInvalid)` 映射 `larkWorkspaceErrorInvalidSkillInput`。nil page、`ErrSkillReadFailed` 和自动续页错误继续映射 `larkWorkspaceErrorSkillRead`。

- [ ] **Step 4: 增加脚本化 fake 和边界测试**

测试两页完整拼接、内部第二次请求的 opaque cursor/canonical reference、旧真实 cursor、重复 cursor、第三页、第二页 invalid、skill/path 漂移、仅 References 漂移、最终 JSON 因转义或 references 超 64 KiB、不泄漏第一页正文/cursor/receipt。更新现有返回固定非空 cursor 的 fake case，避免无限返回同页。

- [ ] **Step 5: 跑 Agent focused tests 并提交**

```bash
go test ./internal/numind/biz/agent -run 'TestLark.*' -count=1
go test ./internal/numind/biz/feishu -run 'TestSkillReader_' -count=1
git add internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go
git commit -m "fix(agent): make Lark skill reads platform-tolerant"
```

Expected: PASS；模型可见 JSON 无 cursor/receipt；超限和内部异常固定 fail closed。

### Task 4: 对齐原子信封第二道防线

**Files:**
- Modify: `internal/numind/biz/agent/runner_v2_artifact.go`
- Modify: `internal/numind/biz/agent/runner_v2_artifact_test.go`

- [ ] **Step 1: 让 tool 与 wrapper 共享最终信封 SOT**

保留 `larkSkillReadAtomicOutputLimit = 64 << 10` 作为同 package 唯一常量；工具聚合和 `boundedAtomicSkillTool` 都引用它。更新 wrapper 注释，删除 model-visible cursor 描述并说明分页已经在可信工具内完成。

- [ ] **Step 2: 补 wrapper 和 adapter 回归**

断言聚合后且上限内的可信 `lark_skill_read` 完整 inline；超限在工具层和 wrapper 都 fail closed、不落 artifact；同名 mock 仍走普通 artifact。复用现有 adapter recoverable 断言证明 `invalid_skill_input` 是 progress 而不是 terminal error。

- [ ] **Step 3: focused 验证并提交**

```bash
go test ./internal/numind/biz/agent -run 'TestWrapToolWithV2ArtifactProcessing|TestLark.*Recoverable' -count=1
git add internal/numind/biz/agent/runner_v2_artifact.go internal/numind/biz/agent/runner_v2_artifact_test.go
git commit -m "refactor(agent): align complete Lark skill envelopes"
```

Expected: PASS，且 Task 4 完成后仓库仍独立编译、工具注册不变。

### Task 5: 全量质量门禁、双审查、原子合并和 Dev 部署

**Files:**
- Create: `docs/superpowers/qa/2026-07-19-lark-skill-reader-tolerance-s5-acceptance.md`
- Modify: `.ndf/manifest.yaml`

- [ ] **Step 1: 格式、全测、lint 和 race**

```bash
gofmt -w internal/numind/biz/feishu/skill_reader.go internal/numind/biz/feishu/skill_reader_test.go internal/numind/biz/agent/tool_lark_skill_read.go internal/numind/biz/agent/tool_lark_personal_workspace_test.go internal/numind/biz/agent/runner_v2_artifact.go internal/numind/biz/agent/runner_v2_artifact_test.go
go test ./...
PATH="$(go env GOPATH)/bin:$PATH" task lint
go test -race ./internal/numind/biz/agent/... ./internal/numind/biz/feishu/...
git diff --check
```

Expected: 所有命令退出 0，无 race report。

- [ ] **Step 2: 并行双 reviewer**

独立 spec reviewer 检查设计 §2-§8、客户 RED commit 顺序和全部安全不变量；独立 quality reviewer 检查错误链、分页有界性、JSON 信封、并发、泄漏和测试可信度。任何 P0/P1/P2 在 feature branch 修复并重审到清零。

- [ ] **Step 3: 写 S5 QA 记录并提交**

QA 必须记录精确命令、通过数量、race/lint、review 结论、未改前端的证据以及 Dev 验收提示词。更新 manifest 为 S5、completed_tasks=5、reviewed_tasks=2。

- [ ] **Step 4: 原子合并与推送**

```bash
bash scripts/ndf/ndf-done.sh
```

Expected: feature 合并到 `develop` 并 push，worktree 和本地 feature branch 被清理。

- [ ] **Step 5: 部署并验证 Dev server**

在主仓 `numind-server`：

```bash
bash scripts/cicd/release.sh dev server
curl -fsS http://49.233.219.254:9091/healthz
```

Expected: 新 `develop-<sha>` 镜像运行且 healthy，healthz 返回 `code:0/status:ok`，启动日志无 panic/fatal，容器内 lark-cli 仍为 1.0.68。

- [ ] **Step 6: 记录 S6 部署**

在 develop 的 manifest 写入 merge SHA、镜像/digest、健康检查和真实 Agent 测试提示；commit/push 文档。Prod 保持不变。

## Spec coverage self-review

- 模型协议与滚动兼容：Task 1、Task 3。
- 精确字段纠错和当前技能白名单：Task 1、Task 2。
- 平台分页、64 KiB 信封、循环/第三页/部分内容保护：Task 3。
- recoverable 与 terminal 错误：Task 1、Task 3。
- 安全不变量和 adversarial cases：Task 2、Task 3。
- 原子 wrapper 第二道防线：Task 4。
- 全量门禁、双审查、合并和 Dev：Task 5。
- 无 DB/API/前端/新 LLM 调用：所有任务均未包含相关文件。
