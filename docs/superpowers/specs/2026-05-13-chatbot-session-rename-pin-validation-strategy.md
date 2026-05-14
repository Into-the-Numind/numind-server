# Chatbot 会话改名与置顶 — S5 验证策略

- **Feature ID**: `chatbot-session-rename-pin`
- **NDF Stage**: S5 (验收策略，T8 task 输出，NDF Rule 10 强制末尾 task)
- **Created**: 2026-05-13
- **Source Plan**: `numind-server/docs/superpowers/plans/2026-05-13-chatbot-session-rename-pin-plan.md` §3 (Task 8)

---

## §1 验证方式（锁定）

**Playwright E2E + 后端 Go test + 前端 Vitest 三件套**（非 gstack `/qa` 一次性验证）

## §2 选择理由（NDF Rule 10 必含）

1. **前端交互复杂**（hover + dropdown + inline modal + pessimistic UI 重排）→ 需 E2E 自动化覆盖
2. **后端 API 契约变化**（新加 2 端点 + 改 1 端点）→ 需 store/biz 单元测试 + Playwright 串联验证
3. **`updated_at` 不刷新（D2）是核心不变量** → E2E 需读取 API response 验证（详 §3 用户路径 #6）
4. **SQL 排序逻辑首次引入 `pinned_at IS NULL ASC`** → 需 store unit test 3-行 case 验证 + E2E 视觉位置验证
5. **不选仅 gstack `/qa` 的理由**：feature 未来修改时需要回归保护（特别是改名 / 置顶逻辑，涉及前后端契约）。gstack `/qa` 一次性验证不产生持久化测试代码

## §3 关键用户路径（S5 必须验证的具体操作步骤）

每条路径都是必跑项。共 **10 条**：

1. **改名 happy path**：登录 → 进入某 chatbot 对话页 → hover session → 点「⋯」→ 点重命名 → 输入新名 → 保存 → 列表显示新名 → API response 200 + body `{id, title}`

2. **改名空白校验**：菜单 → 重命名 → 输入纯空白 → 保存 → toast 警告 "标题不能为空"（前端 trim 校验，不发请求）

3. **置顶 happy path**：菜单 → 点置顶 → session 移到列表顶部 + 左侧 2px primary 边框 → DevTools Network 看 PUT response `pinned_at` 非 null

4. **重复置顶**：先置顶 session A 再置顶 session B → B 移到列表顶部（B 的 pinned_at 比 A 更新）

5. **取消置顶**：菜单 → 点取消置顶 → session 离开置顶组回到 updated_at 排序位置

6. **`updated_at` 不变量验证**（核心 D2）：
   - 改名 / 置顶 / 取消置顶**前**用 GET `/v1/chatbot/sessions?chatbot_id=X` 记下 `updated_at` 时间戳
   - 执行操作
   - 再 GET 看 `updated_at`
   - 前后值应**完全相等**（精度到秒）

7. **删除菜单迁移验证**：菜单内点删除 → 现有 ConfirmModal 出现 → 确认 → session 从列表消失（行为与旧版完全一致，仅触发入口从 hover trash icon 改为「⋯」菜单内的"删除"项）

8. **跨 chatbot 隔离**：在 chatbot A 改名 session X → 切换到 chatbot B → B 的 session 列表**不**含 X 的改名痕迹（chatbot_id 查询参数生效，cross-chatbot 隔离）

9. **未登录 401**：直接调 `PUT /v1/chatbot/sessions/:id/rename` 不带 token → 401 `ErrTokenInvalid`

10. **非本人 403**：用 user A token 改 user B 的 session → 403 `ErrForbidden`（手动 SQL 制造另一个用户的 session + curl 验证）

## §4 测试文件位置

### Playwright E2E
- `numind-web-v3/e2e/chatbot-session-rename-pin.spec.ts`（新建）
- 至少覆盖路径 #1, #2, #3, #4, #5, #6, #7, #8（前端可达的 8 条 — 含 #2 空白校验 toast 验证 + #8 跨 chatbot 隔离）

### Backend Go test
- `numind-server/internal/numind/store/chatbot_session_test.go`（T2 已完成，10 unit tests）
- `numind-server/internal/numind/biz/chatbot/chatbot_test.go`（T3 已完成，8 unit tests）

### Frontend Vitest
- `numind-web-v3/src/stores/__tests__/chatbot.spec.ts`（T6 已完成，8 unit tests）

### 后端 curl 验证（路径 #9 / #10）
S5 阶段在 dev 环境用 curl 手动验证未登录 401 + 非本人 403 两条路径（无法用 Playwright 模拟伪造身份）

## §5 重复置顶 → pinned_at 刷新的验证方式

（S2 holistic reviewer P2-NEW-2 锁定方案）

E2E 测试用**双重验证**：
1. **API response 比较**：先置顶 A 记录 `pinned_at_A`；再置顶 B 看返回的 `pinned_at_B`；断言 `pinned_at_B > pinned_at_A`（时间戳后写覆盖前写）
2. **UI 位置变化**：DOM 查询验证 B session 在置顶组的**第一位**（A 退到第二位）

任一断言失败即 E2E fail。

## §6 S5 执行顺序

按以下顺序在本地（feature 分支）执行：

```bash
# 1. 应用 migration 到本地 dev DB
cd numind-server-rename-pin  # worktree
# 手动 SSH 到 dev DB 执行 forward migration SQL（或本地 docker MySQL 测试）

# 2. 启动本地后端
task dev

# 3. 启动本地前端
cd ../numind-web-v3-rename-pin  # worktree
npm run dev

# 4. 跑后端单元测试（重跑全测套确保不退化）
cd ../numind-server-rename-pin && task test

# 5. 跑前端单元测试
cd ../numind-web-v3-rename-pin && npm run test:unit

# 6. 跑 Playwright E2E
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e

# 7. 手动 curl 验证 #9 #10 路径
curl -X PUT "http://localhost:9091/v1/chatbot/sessions/1/rename" \
  -H "Content-Type: application/json" -d '{"title": "test"}'  # 期望 401

curl -X PUT "http://localhost:9091/v1/chatbot/sessions/1/rename" \
  -H "Authorization: Bearer $OTHER_USER_TOKEN" \
  -H "Content-Type: application/json" -d '{"title": "test"}'  # 期望 403
```

## §7 S5 验收完成标准

S5 gate 通过条件（NDF §3 S5）：

- [ ] 后端 `task test`（完整版含 race detection + coverage）退出码 0
- [ ] 后端 `task lint` 退出码 0
- [ ] 前端 `npm run lint`、`npm run type-check`、`npm run test:unit` 全部退出码 0
- [ ] `npm run test:e2e` 退出码 0（chatbot-session-rename-pin.spec.ts PASS）
- [ ] 手动 curl 验证未登录 / 非本人路径返回正确 HTTP code
- [ ] 浏览器 QA: AI 审查 Playwright 截图，无 P0 级视觉/功能回归
- [ ] 可观测性 N/A（本 feature 不涉及 LLM 调用，参 spec §3 AI 可观测性 N/A 声明）

## §8 与 spec / plan 对应

| Spec / Plan 节 | 验证策略对应 |
|---------------|------------|
| spec §1 数据模型 | §6 dev DB migration apply |
| spec §2 API 契约 | §3 路径 #1, #3, #5 + §4 curl |
| spec §3 store/biz/controller 实现 | §4 后端单元测试（T2/T3 已完成）|
| spec §4 排序 SQL 兼容性 | §4 store unit test（T2 已含 3-行 case）|
| spec §5 前端设计 | §4 Vitest + Playwright E2E |
| spec §6 边界 case 17 条 | 后端 unit test + E2E + 手动 curl 各覆盖一部分 |
| spec §7 测试策略（建议）| §4 完整落地 |
| spec D2 不变量（updated_at 不刷新）| §3 路径 #6（核心验证）|
| spec EC-14 重复置顶刷新 | §5 双重验证方案 |

## §9 已知限制 / Out-of-scope

- **不做**多设备并发测试（spec EC-12 接受 last-write-wins，不做特殊验证）
- **不做**性能压测（feature 是 metadata 操作，无性能风险）
- **不做** Langfuse trace 验证（本 feature 不涉及 LLM 调用）
- **不做**生产数据迁移演练（migration 是纯加列 NULL，零 backfill 风险，S6 上线时直接 apply 即可）

---

**T8 实施说明**：本文件是 plan §3 内容物质化为独立 markdown 的产物。S5 阶段直接按本文件执行验证。
