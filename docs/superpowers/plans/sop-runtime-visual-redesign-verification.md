# sop-runtime-visual-redesign · S5 验证执行剧本

> **NDF Rule 10 要求的最后独立 task (V1) 产出**。
> 本文件是 S5 阶段的执行剧本，不是代码。固化自 plan §4。

---

## 1. 验证方式

组合策略，**三层验证**：

| 层 | 工具 | 性质 | 覆盖范围 |
|---|---|---|---|
| **业务回归** | Playwright E2E | 持久化（spec 文件入库） | 高风险业务逻辑路径 P1/P2/P3/P4/P5 |
| **视觉对比** | gstack `/qa` | 一次性（截图不入库） | 6 个状态视觉与 mockup 比对 P6 |
| **后端契约** | curl + jq | 一次性 | meta 字段透出验证 P7 |

### 1.1 三层选择理由

- **Playwright 覆盖会引发回归的业务逻辑**：bookmark 涉及配额扣减 + 持久化，重新生成涉及覆盖式写入 sop_node_run，trailing chat 涉及独立流式协议。这些代码逻辑变化必须有自动化回归保护。
- **gstack `/qa` 覆盖纯视觉层面**：CSS 变化没有 LOC 代码可以断言，唯一的真相源是 mockup HTML。一次性截图 + 视觉对比成本最低。
- **curl 复验后端字段**：直接打 API 验证 DTO 是否正确透出 model_name / duration_ms / total_tokens，免依赖前端，最快。

---

## 2. 关键用户路径

| ID | 路径描述 | 验证方式 | 覆盖状态 | 阻塞条件 |
|---|---|---|---|---|
| **P1** | 进入无 runId → state C → 输入文本 → 执行 → state D 流式 → 完成 state E | Playwright E2E (`sop-runtime.spec.ts`) | C, D, E | cross-repo gate 已通过 |
| **P2** | 完成 step 1 → 切换历史 step → state B 只读 → HistoryViewStrip 出现 → 点返回步骤 N → state A | Playwright E2E (`sop-history-view.spec.ts`) | A, B | — |
| **P3** | state E → 点重新生成 → 弹 ConfirmModal → 确认 → 旧 output 抹除 → 重跑 → 新 output 覆盖写入 sop_node_run | Playwright E2E (`sop-runtime.spec.ts` 扩展) | E → D → E | — |
| **P4** | state E → 点 ⭐ 收藏 → 刷新页面 → 新建 run → 自动应用 bookmark → toast "已自动应用 N 个书签" 显示 | Playwright E2E (`sop-bookmark.spec.ts`) | E, C | cross-repo gate（auto_apply 字段） |
| **P5** | 完成全部 SOP 节点 → 进入 trailing chat (state F) → 发消息 → 流式 AI 回复 → MetaFooter 显示模型 + 耗时 | Playwright E2E (新建或扩展) | F | cross-repo gate（chat duration_ms） |
| **P6** | 6 个状态 gstack 截图 → 逐个与 mockup 01/02 比对 | gstack `/qa` 视觉脚本 | A, B, C, D, E, F | local dev server 启动 |
| **P7** | curl `/v1/sop/runs/:id/status` 验证 `completed_nodes[].{model_name,latency_ms,total_tokens}` 字段；curl `/v1/sop/runs/:id/chat-messages` 验证 `messages[].{model_name,duration_ms}` 字段 | curl + jq | — | cross-repo gate 已部署到 dev |

---

## 3. 回归保护诚实声明

### 3.1 持久化层（提供回归保护）

- **Playwright E2E**：P1–P5 五条路径写为 spec 文件入仓库，未来本页面任何修改触发 CI 都会自动跑
- **单元测试**：sopRun.spec.ts (16) + StepNav.spec.ts (19) + OutputCard.spec.ts (10) + MetaFooter.spec.ts (9) 等持续守业务逻辑
- **后端 go test**：B 系列加的字段 + biz 写入逻辑通过现有 sop biz 测试覆盖

### 3.2 一次性层（不产生持久化保护）

- **gstack `/qa` 截图比对**：P6 的视觉对比不入代码仓库，未来视觉修改（例如改个间距 / 改个颜色）需要**手动**重新跑 gstack `/qa`，否则视觉退化无人发现
- **curl 复验**：P7 只在 S5 执行一次，之后字段保护靠 Playwright 间接保证

### 3.3 已知未来回归风险

> **诚实声明**：以下风险**没有自动化保护**，需要 S6 阶段或后续修改时人工注意。

1. **CSS 视觉退化**：如果有人修改 SOP 运行页的 CSS 但不改业务逻辑，Playwright 可能全绿但视觉已跑偏。**缓解**：S6 上线前必须再跑一次 gstack `/qa` 视觉对比。
2. **MetaFooter 字段缺失退化**：如果 B5 DTO 映射回归（例如 controller 重构漏字段），Playwright 不会失败（MetaFooter 缺字段时静默不渲染），只有 P7 能发现。**缓解**：把 P7 作为 PR review checklist 的一部分。
3. **bookmark auto-apply 静默失败**：`auto_applied_count = 0` 时 toast 不会出，用户无感。**缓解**：手动测试 P4 时确认 toast 出现。
4. **trailing chat 加载历史失败 fallback**：F11 P1-3 修复用 reloadTrigger 触发 TrailingChat.loadHistory，如果 listRunChatMessages API 失败，UI 显示空列表（与刷新前一致）。**缓解**：网络异常时 toast 提示。

### 3.4 本 feature 选择的策略

**Playwright + gstack 组合**，理由：
- 视觉变更频率高 + mockup 已作为契约固化 → gstack 截图比对成本最低
- 业务逻辑（bookmark/重新生成/auto_apply）不能依赖一次性验证 → 必须写 Playwright

---

## 4. 验证脚本与命令

### 4.1 前端验证（numind-web-v3）

```bash
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-web-v3

# 基础质量门
npm run lint
npm run type-check
npm run build

# 单元测试
npm run test:unit
# 期望：271+ tests passed (含 F1/F3/F6 新增)

# E2E 全跑
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- \
  e2e/sop-runtime.spec.ts \
  e2e/sop-bookmark.spec.ts \
  e2e/sop-history-view.spec.ts \
  e2e/sop-stop-generation.spec.ts

# 期望：P1/P2/P3/P5 通过；P4 (sop-bookmark) 完整路径 fixme（dev gate 后翻开）
```

### 4.2 后端验证（numind-server）

```bash
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server

# 基础质量门
task lint
go build ./...
go test ./internal/numind/biz/sop/...
# 期望：现有测试不破，无新增测试（B 系列纯字段添加）
```

### 4.3 后端 curl 复验（dev 环境）

```bash
# Step 1: 拿 token
TOKEN=$(curl -sX POST $DEV_API_URL/v1/web/login \
  -H "Content-Type: application/json" \
  -d '{"username":"'$E2E_USERNAME'","password":"'$E2E_PASSWORD'"}' \
  | jq -r .data.token)

# Step 2: 创建新 run + 执行至少一个节点（需要前端或 SSE 客户端）
# 略（gate 步骤里已经做过）

# Step 3: 验证 sop_node_run 字段
curl -s "$DEV_API_URL/v1/sop/runs/$RUNID/status" \
  -H "Authorization: Bearer $TOKEN" \
  | jq '(.data.completed_nodes | length > 0) and
        (.data.completed_nodes[0] | has("model_name") and has("total_tokens") and has("latency_ms"))'
# 期望：true

# Step 4: 验证 sop_chat_message 字段
curl -s "$DEV_API_URL/v1/sop/runs/$RUNID/chat-messages" \
  -H "Authorization: Bearer $TOKEN" \
  | jq '.data.messages | (length == 0) or
        (.[0] | has("model_name") and has("duration_ms"))'
# 期望：true
```

### 4.4 gstack `/qa` 视觉对比

```bash
# 启动 local dev server
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-web-v3
npm run dev &  # 默认 5173

# 用 gstack /qa skill 自动操作浏览器
# 步骤（执行者操作 gstack 浏览器）：
#   1. 登录 $LOCAL_SITE_URL/login
#   2. 进入 $LOCAL_SITE_URL/sop/run?templateId=3 → state C draft 入口 → 截图
#   3. 输入 prompt + 点执行 → 等待 → state D 流式 → 截图
#   4. 等待完成 → state E 完成 → 截图
#   5. 点 step 2 nav → state A 默认 → 截图
#   6. 完成 step 1 后点 step 1 nav → state B 历史只读 → 截图
#   7. 完成全部主流程 → 进入 step 4 trailing chat → state F → 截图
#   8. 截图归档到 numind-server/proposals/sop-runtime-visual-redesign-screenshots/
#
# 然后人工对照 numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html
# 和 02-additional-states.html 逐状态比对：
#   - 间距 / 字号 / 颜色一致？
#   - 元素位置 / 层次一致？
#   - hover / focus 态视觉对吗？
```

### 4.5 验收 checklist

- [ ] 前端 lint / type-check / build / 单测全绿
- [ ] 后端 lint / build / 现有测试全绿
- [ ] cross-repo gate 完成（B1-B5 已 merge develop + dev 部署 + curl 验证 P7 通过）
- [ ] Playwright E2E：sop-runtime / sop-bookmark / sop-history-view / sop-stop-generation 全绿（fixme 路径除外）
- [ ] gstack `/qa` 6 状态截图归档 + 人工视觉比对通过
- [ ] 任何视觉退化记录在 issue 或后续 hotfix 中

---

## 5. S5 → S6 进入条件

满足以下全部 → 进入 S6 上线流程：

1. ✅ 所有 20 个 task (B1-B5 + F0-F13 + V1) 完成且双 review PASS
2. ✅ Cross-repo gate 通过
3. ✅ 本文件第 4.5 节验收 checklist 全勾
4. ✅ 已知未来回归风险列表（§3.3）已经记录到 manifest 的 deferred 字段或独立 follow-up

---

## 6. 工件引用

- **requirement (S0)**: `numind-server/requirements/sop-runtime-visual-redesign.md`
- **proposal (S1)**: `numind-server/proposals/sop-runtime-visual-redesign-proposal.md`
- **backend audit**: `numind-server/proposals/sop-runtime-visual-redesign-backend-audit.md`
- **mockups**:
  - `numind-server/proposals/sop-runtime-visual-redesign-mockups/01-active-and-history.html`
  - `numind-server/proposals/sop-runtime-visual-redesign-mockups/02-additional-states.html`
- **spec (S2)**: `numind-server/docs/superpowers/specs/2026-04-11-sop-runtime-visual-redesign-design.md`
- **plan (S3)**: `numind-server/docs/superpowers/plans/sop-runtime-visual-redesign-plan.md`
- **gate review**: `numind-server/proposals/sop-runtime-visual-redesign-s2s3-review.md`
- **本文件 (V1)**: `numind-server/docs/superpowers/plans/sop-runtime-visual-redesign-verification.md`
