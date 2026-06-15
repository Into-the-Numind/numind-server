# 通知系统（Notification Center）— S5 QA 报告

> 2026-06-16 · feature/notification-center · 11/11 编码任务完成，每个任务双 Sonnet review 通过（P0/P1 全修）。

## 自动化验证结果

### 后端 numind-server
- `go build ./...` → exit 0
- `go test ./...`（全仓库）→ **0 FAIL**
- `task lint`（go vet + golangci-lint）→ exit 0
- 单测覆盖（biz + store）：可见性过滤、已读 upsert 幂等、一人一答、答卷四类校验、答卷事务回滚（submitCalls=1/markReadCalls=0）、read_rate/response_rate（target=0 边界）、GORM default:1 bool 回归（去 fixup 则 FAIL，已验证 load-bearing）、readers read/unread 反连接（admin/软删用户排除）、survey 聚合。

### 管理端 numind-admin-web
- `npm run type-check`（vue-tsc）→ exit 0
- `npx vitest run`：本功能测试全过（announcement store 16 + SurveyQuestionBuilder 7 + SurveyResultChart 9）。全仓库 109 tests passed；**1 个预存无关 suite 失败**：`src/stores/__tests__/agent.spec.ts` import `../agent`（`src/stores/agent.ts` 在 develop baseline 不存在，6/11 起就 broken，属其它并行 feature 的 orphan 测试，**不在本 feature diff 内，未触碰**）。
- `npx eslint`（本功能文件）→ exit 0

### 用户端 numind-web-v3
- `npm run type-check`（vue-tsc）→ exit 0
- `npx vitest run`：全仓库 **83 文件 / 897 tests passed**（含 SurveyFillForm 4 + announcements 5），0 fail。
- `npx eslint`（本功能文件）→ exit 0

## 验证策略执行（对照 spec §7 / S3 plan T9）
- ✅ 后端 Go 单测（持久回归）— 已跑通。
- ✅ 前端 vitest store/组件单测 — 已跑通。
- ✅ 两端 type-check + scoped lint — 0。
- ⏳ **Playwright E2E（关键用户路径）**：`e2e/notification-center.spec.ts` 已随代码留存（覆盖 铃铛红点出现/读后递减、问卷提交→二次进入已提交只读态、flag 隐藏铃铛），但**完整浏览器闭环未在本 session 跑**——需起本地全栈（MySQL+Redis+server+两前端），受并行任务/本地环境约束未起栈，且未授权部署。
  - **诚实声明（spec §7 既定策略，S3 已批准）**：本功能无支付/会员等级高风险逻辑；持久自动回归由 Go 单测 + 前端 vitest 提供；浏览器闭环在 **dev（flag 仅 dev 开）由 gstack /qa 验证**，或本地起栈后跑 E2E spec。

## 隔离性核验（用户核心诉求："不影响 prod 打 tag"）
- feature flag `features.notification_center.enabled` 代码默认 **OFF**；`config_prod.yaml` / `config_qa.yaml` **未写该 key** → prod/qa 自动关闭。
- 后端路由（user + admin announcement group）挂 `FeatureFlag` 中间件，flag off → `ErrFeatureDisabled`（404）。
- 前端铃铛受 `VITE_ENABLE_NOTIFICATIONS` 控制；`.env.production` 未设 → prod 构建隐藏铃铛。
- 全新独立 5 表 + 独立 migration，**零改动现有表/端点**（仅新增）。
- 结论：合入 develop 后，他人打 prod tag 上线其它功能时，通知系统**完全休眠**，不暴露、不影响。

## S6 上线前置（启用时需做，非本 session）
1. 部署后 AutoMigrate 自动建 5 张表（模型已注册 helper.go）。
2. **FK/UNIQUE 约束需手工执行** `migrations/20260616_120000_create_notification_center.sql`（dev-deploy 不自动跑 migration，见 dev-deploy-migration-gap）。
3. 启用：在目标环境 config 加 `features.notification_center.enabled: true` + 前端构建设 `VITE_ENABLE_NOTIFICATIONS=true`，重启。
