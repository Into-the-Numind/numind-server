# QA Report — parent-self-grant-membership

- **NDF Stage**: S5 (Auto-Verification, degraded per user decision)
- **Date**: 2026-04-20
- **Deviation from NDF §3 S5**: E2E 本地跑被 defer 到 S6 dev CI，理由见 §5

## 验证环境

- 后端：**未启动本地** — feature 分支代码跑 unit test only（SQLite in-memory）
- 前端：**未启动本地** — 仅做 lint + type-check + Playwright spec 语法校验
- 浏览器：**未执行** — gstack /qa 延后
- 决策：用户选 Option B（规避 dev DB 污染风险，defer E2E 到 S6 post-merge dev 环境）

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go build | `go build ./...` | PASS | exit 0（仅无关 sqlite-vec CGO macOS 弃用警告）|
| Go vet | `go vet ./internal/numind/...` | PASS | 无输出 |
| gofmt | `gofmt -l <4 改动文件>` | PASS | 无输出 |
| Go test（新增） | `go test ./internal/numind/biz/credit/ -run TestGrantMembership` + `./internal/numind/store/` | PASS | 15 biz + 1 store = **16 tests PASS** |
| Vue lint（web-v3） | `npm run lint` | PASS | 0 errors（1 pre-existing warning in `KnowledgeBaseDetail.vue` 与本功能无关）|
| Vue type-check（web-v3） | `npm run type-check` | PASS | vue-tsc exit 0 |
| Admin lint | N/A | N/A | 本功能不涉及 admin |
| Playwright spec 加载 | `npx playwright test --list --grep=parent-self-grant` | PASS | 3 tests + setup 可加载，语法有效 |
| E2E 实际执行 | `npm run test:e2e` | **DEFERRED** | Option B decision — 跑在 S6 dev 环境 |
| gstack /qa 截图 | N/A | **DEFERRED** | 同上 |
| `task lint` | `task lint` | SKIPPED | 本地无 `task` 二进制；用 `go vet` + `gofmt` 等效覆盖，CI 在 S6/S7 会跑 |
| `task test`（完整含 race） | `task test` | SKIPPED | 同上；targeted `go test` 已 PASS |

## 浏览器 QA

**DEFERRED** 到 S6 post-merge dev 环境验证。

## 可观测性验证

**N/A** — 本功能不涉及 LLM 调用。

## 预存在问题（非本功能引入）

| 测试 | 状态 | 验证 |
|------|------|------|
| `TestReserve_ExactlyExhaustedThenRetry_ReturnsInsufficientSentinel` | FAIL | 在 develop base `8c7aec2` 就失败。`panic: unreachable: legacy_tier must be guarded by SkipDeduction` at `credit_service.go:164`。已在 manifest 登记 defer，建议独立 hotfix 处理 |

## PRD 验收标准核对（参照 `proposals/parent-self-grant-membership-proposal.md` §4）

| PRD 验收标准 | S5 状态 | 证据 / 备注 |
|------|------|------|
| **前端 7 项**：列表显示自己 + action 菜单 + 弹窗 + 成功 toast + 列表刷新 + 不出现支付二维码 | **DEFERRED** | Playwright spec 已写，S6 dev CI 实跑验证 |
| **后端 7 项**：API 契约扩展 + credit_package/action_log 字段 + sub-users 含父 + trial 防重 + monthly 防重 + billing_mode 切换 | **PASS** | 16 个单测覆盖，后端 TDD 路径完整 |
| **越权防线 3 条**：子不能自开 / 父跨父开拒 / child_id 不存在 | **PASS** | `TestGrantMembership_SubUserSelfGrant_Rejected` / `TestGrantMembership_CrossParentGrant_Rejected` / `TestGrantMembership_ChildNotExists_Rejected` 均 PASS |
| **账务 2 项**：报表聚合 self-grant 记录到 granter_user_id | **PASS (inferred)** | spec §3.5 SQL 语义分析，本 feature 不改报表 SQL，现有聚合对 self-grant 自动生效。S6 dev 验证首次运行时可人工拉一次报表确认 |

## 结论

**PARTIAL_PASS_WITH_DEFERRAL**

- ✅ 所有 static 可执行的验证全 PASS（16 新测试 + 前后端 lint/build/type-check + Playwright spec 加载）
- ⚠️ 本地 E2E 实际执行延后到 S6 dev 环境（用户 Option B 决策，规避 dev DB 风险）
- 📝 S6 人工验收时必须在 dev 环境**实跑 E2E + 手工验证关键路径**（见下文 S6 验收步骤）

## 失败项修复要求

无失败。静态检查全绿。

## S6 验收补齐清单（因 S5 降级产生）

S6 merge 到 develop 后，dev CI 部署完成，以下步骤替代本地 E2E：

1. **登录 dev 环境**（`49.233.219.254:9200`，用 `$E2E_USERNAME` / `$E2E_PASSWORD`）
2. **访问 `/customers` 页面**，确认自己账号出现在第一行
3. **点击自己行 action 菜单** → "帮开通会员" → 选 trial → 确认
4. **验证**：
   - Toast 显示"已为 {自己昵称} 开通体验会员"
   - 列表刷新，自己行会员状态更新
   - （后台）`credit_package` 表新增一行 `user_id=自己ID, granter_user_id=自己ID, grant_source='b2b_grant', type='trial'`
   - （后台）`action_log` 表新增一行 `user_id=target_id=自己ID, action='grant_membership'`
5. **子账户回归**：点击任意子账户行 → "帮开通会员" → monthly 1 月 → 确认 → 同样验证数据写入正确
6. **防越权人工验证（可选）**：用 curl 直接 POST `/v1/users/children/{parent_B_id}/grant-membership` → 返回 403

**本清单成为 S6 人类验收的具体指引**（替代 S5 未执行的本地 E2E）。
