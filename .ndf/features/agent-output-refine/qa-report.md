# QA 报告 — agent-output-refine (S5)

> NDF Standard S5。日期 2026-06-16。分支 feature/agent-output-refine（双 worktree：server + web-v3）。

## 1. 范围（7 项 dev 反馈）
#1 思考块箭头方向 / #2 文件图片命名 / #3 单图 S2+多图 M1 / #4 标题分级 / #5 表格引用翠绿 / #6 过滤 ask_user_question 转圈行 / #7 问题卡 answered 态 C3 统一。

## 2. 自动化门禁（全过）
| 仓库 | 检查 | 结果 |
|------|------|------|
| numind-server | `go test ./internal/numind/biz/agent/` | **0 FAIL** |
| numind-server | golangci-lint(agent 包) | 0 finding |
| numind-server | gofmt | clean |
| numind-web-v3 | `npx vitest run`（全量） | **888 passed / 81 文件**, 0 FAIL（15 skip / 3 todo 为既有）|
| numind-web-v3 | `vue-tsc -p tsconfig.app.json` | 0 |
| numind-web-v3 | eslint(全部改动文件) | 0 |

新增/更新测试：
- `tool_image_gen_test.go`：`defaultImageFilename` 格式 + 无 "gemini" 守卫。
- `agentArtifacts.spec.ts`：#2a alt 优先（image+doc）/空 alt 回退/`groupAdjacentImages`（连续 3 图并组、单图保持、图-文-图不并、doc 不进组、收尾 flush）。
- `AgentFinalAnswer.spec.ts`：多图→1 个 `.image-grid`（3 img）、单图→不进 grid 走卡片、命名为 alt。
- `AgentArtifactItem.spec.ts`：S2 无 doc-badge/file-row 外框正向断言。
- `AgentToolCallList.spec.ts`：过滤 ask_user_question（混合只剩普通 / 全过滤无 timeline）。
- `QuestionPrompt.spec.ts`：answered 态 emerald avatar + 「已回答」。
- `ThinkingBlock.spec.ts`：`lucide-chevron-right` base 图标。

## 3. 双 Sonnet review（两阶段，全 PASS）
- **spec-compliance**：PASS，0 P0/P1。逐条核对 7 项落点与 spec 一致；确认非 customer/prod bug → Rule 11 不适用；commits 全 Conventional（feat/fix）。1 P2（AgentArtifactItem spec 缺 S2 正向断言）→**已修**。
- **code-quality**：PASS，0 P0。1 P1（grid img 无 @error，COS 过期→碎图）→**已修**（@error 调暗 + title + 稳定 key）。4 P2（displayName 守卫注释/grid key/answered avatar 尺寸统一/image_gen 用 local 时区）→ 前 3 项**已修**，时区项为文件名展示无需 UTC 归一（不修，已记理由）。
- 修复后重跑全量 vitest + tsc + eslint 全绿。

## 4. 运行时验收（S6 dev，按 plan T-verify）
完整 agent run 需 dev 后端+LLM+积分，本地不可复现。S6 ndf-done + /deploy-dev(server 先于 web)后于 dev browse 实跑：
1. #1：思考块折叠→箭头指右▶、展开→指下▼。
2. #2：生成的文件/图片显示可读名（markdown alt/链接文字），不再是下划线 / gemini-image-{unix}。
3. #3：单图无大外框（轻阴影+点击放大）；多图自适应网格 M1。
4. #4：h1/h2 衬线分级、字阶微妙、段间距分级。
5. #5：表格表头/引用/内联代码/链接为柔和翠绿（内联代码不再刺眼红）。
6. #6：问题答完后时间线**无**「等你回答一个问题」转圈行。
7. #7：问题卡 answered 态 emerald avatar+衬线回看，与 asking C3 同一家族。

## 5. 回归保护诚实声明
逻辑/结构（命名 alt 优先、groupAdjacentImages 分组、过滤 ask_user_question、分段就地、answered 结构、chevron base 图标）有 vitest 持久保护；纯视觉精细度（箭头角度、字阶、翠绿浓淡、S2/M1/C3 观感）为一次性 dev 确认（无自动回归，可接受——非支付/权限高风险）。

## 6. S6 部署
server 先于 web；TCR 100-tag 上限可能触发（满则清旧 develop 标签后重部署）。
