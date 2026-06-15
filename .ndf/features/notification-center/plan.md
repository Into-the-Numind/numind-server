# 通知系统（Notification Center）— 实施计划 Plan

> 基于 spec.md v1.0。后端 task 在前（API 契约源），前端两仓库按锁定契约并行（Tier 2，跨仓库 disjoint write）。

## 任务依赖图
```
T1(foundation) → T2(store) → T3(biz+tests) → T4(controller+routes)
                                                   │ (契约已由 spec 锁定，实测需后端)
                          ┌────────────────────────┴────────────────────────┐
                  admin-web:  T5(api+store) → T6(views)        web-v3: T7(api+store) → T8(UI+e2e)
                          └──────────── Tier 2 跨仓库并行 ───────────────────┘
T9: S5 验证策略（独立 reviewer 一并审）
```
无环。后端串行（store→biz→controller 层层依赖）。admin-web 链与 web-v3 链跨仓库可并行（Tier 2）。

---

## T1 — 后端基础层（foundation）
**仓库**：numind-server
**文件**：
- `internal/pkg/model/announcement.go`（5 个 GORM model：Announcement / AnnouncementRead / SurveyQuestion / SurveyResponse / SurveyAnswer，按 spec §1，含 tag/索引/TableName/软删/datatypes.JSON）
- `migrations/20260616_*_create_notification_center.sql`（CREATE TABLE IF NOT EXISTS ×5 + FK + UNIQUE + 索引，idempotent，utf8mb4）
- 注册模型到 AutoMigrate 列表（grep `AutoMigrate` 定位）
- `internal/pkg/errno/notification.go`（spec §4 六个错误码）
- `internal/pkg/middleware/feature_flag.go`（`FeatureFlag(key string) gin.HandlerFunc`，flag off → ErrFeatureDisabled + Abort）
- `config_local.yaml` + `config_dev.yaml` 加 `features.notification_center.enabled: true`（**不碰 config_prod.yaml**）
**验收**：`task lint` 退出 0；`go build ./...` 通过；模型已进 AutoMigrate；migration SQL 语法正确。
**Tier**：单独执行（后续 task 依赖）。

## T2 — 后端 store 层
**仓库**：numind-server
**文件**：`internal/numind/store/announcement.go`（`IAnnouncementStore` + 实现）+ 注册到 store 聚合（`IStore`/datastore）+ `internal/numind/store/announcement_test.go`
**方法**（按 spec §3/§5）：用户端列表(可见性过滤+分页)/详情(含问题)/未读数/已读 upsert/提交答卷(事务)/查是否已提交；admin CRUD/publish/archive/软删/列表(含计数)/stats 计数(target/read/response)/readers(read & unread 反连接,分页)/survey 聚合(option_counts,rating 分布+avg,text 列表)/responses(按用户,分页)。target_count=COUNT(user is_admin=false,未删)。
**验收**：store 单测（in-memory sqlite，AutoMigrate 建表）green；`go test ./internal/numind/store/...` 退出 0；`task lint` 0。
**Tier**：依赖 T1。

## T3 — 后端 biz 层 + 单测
**仓库**：numind-server
**文件**：`internal/numind/biz/announcement/announcement.go` + `survey.go` + 接口注册到 biz 聚合 + `*_test.go`
**逻辑**：可见性判定；已读幂等；一人一答（先查后插+唯一键兜底）；答卷校验（required/题型匹配/选项越界/rating 越界）；read_rate / response_rate 计算（target=0 边界）；GORM default bool（is_important/required 用 *bool 或 fixup）。
**验收**（spec §7 后端清单）：biz 单测覆盖 可见性过滤 / 已读幂等 / 一人一答 / 答卷校验 4 类 / 速率计算边界 / default bool 落库；`go test ./internal/numind/biz/announcement/...` green；`task lint` 0。
**Tier**：依赖 T2。

## T4 — 后端 controller + 路由
**仓库**：numind-server
**文件**：`internal/numind/controller/v1/announcement/user.go` + `admin.go`（+ DTO/request 结构）；改 `internal/numind/router.go`（user group + FeatureFlag 中间件）；改 `internal/numind/admin_router.go`（admin group + FeatureFlag 中间件）；controller 接到 biz 聚合入口（如 `b.Announcement()`）。
**验收**：所有 spec §3 端点注册；`task lint` 0；`go test ./...` 退出 0；`task build` 通过；controller 只做绑定/鉴权/响应（无业务逻辑，硬规则）。
**Tier**：依赖 T3。

## T5 — admin-web API + store
**仓库**：numind-admin-web
**文件**：`src/api/announcements.ts`（类型 + 16 个端点薄封装）+ `src/stores/announcement.ts`（setup 语法，仿 complianceRule.ts）+ `src/stores/__tests__/announcement.spec.ts`
**验收**：`npm run type-check` + `npm run lint` 0；store 单测 green。
**Tier**：依赖 T4 契约（spec 已锁，可在后端完成后启动）。

## T6 — admin-web 视图
**仓库**：numind-admin-web
**文件**：`src/views/announcement/AnnouncementListView.vue`（DataTable）+ `AnnouncementFormView.vue`（新建/编辑 + Markdown 提示 textarea）+ `AnnouncementStatsView.vue` + `src/components/announcement/SurveyQuestionBuilder.vue` + `SurveyResultChart.vue`（div bar）+ 改 `src/router/index.ts`（4 路由）+ `AdminSidebar.vue`（菜单项）+ `SurveyQuestionBuilder.spec.ts`
**验收**：列表用 DataTable（硬规则）；删除/归档走 ConfirmModal；blur 校验；4 态；`npm run type-check` + `npm run lint` 0；组件单测 green。
**Tier**：依赖 T5（同仓库串行）。

## T7 — web-v3 API + store
**仓库**：numind-web-v3
**文件**：`src/api/announcements.ts`（类型 + 用户端 5 端点）+ `src/stores/announcements.ts`（**勿混 toast notifications.ts**）+ `src/stores/__tests__/announcements.spec.ts`
**验收**：`npm run type-check` + `npm run lint` 0；store 单测 green。
**Tier**：依赖 T4 契约；与 T5/T6 跨仓库并行（Tier 2）。

## T8 — web-v3 UI + E2E spec
**仓库**：numind-web-v3
**文件**：铃铛入口（全局布局/Sidebar，按实际 layout 定位）+ 未读红点 + 通知中心（列表面板或 `NotificationsView.vue`）+ 详情（`useMarkdown` 渲染）+ 问卷作答表单 + `.env.development` 加 `VITE_ENABLE_NOTIFICATIONS=true` + 改 router（如走路由）+ `e2e/notification-center.spec.ts`
**验收**：铃铛受 `VITE_ENABLE_NOTIFICATIONS` 控制；4 态；进入详情标记已读；问卷一人一答只读态；`npm run type-check` + `npm run lint` 0；store/组件单测 green；E2E spec 文件存在。
**Tier**：依赖 T7（同仓库串行）；与 admin-web 链并行。

## T9 — S5 验证策略（独立 reviewer 一并审，Rule 10）
**验证方式**：后端 Go 单测（持久回归）+ 两前端 lint/type-check/vitest + Playwright E2E spec（关键用户路径）+ 本地能起栈则 gstack /qa。
**理由**：本功能含数据完整性逻辑（已读回执幂等、一人一答、答卷校验、速率统计），后端 Go 单测是最强回归保护，必须有。前端关键路径（读公告→红点变化→答卷提交→已提交态）写 Playwright E2E spec 留存。无支付/会员等级高风险逻辑，故不强制全量浏览器 E2E 必须当场跑通——但 E2E spec 必须随代码留存提供回归脚本。
**回归保护诚实声明**：Go 单测 + 前端 vitest 提供持久自动回归；Playwright E2E spec 留存但完整浏览器跑通受本地环境/并行任务约束，若本地无法起栈则在 dev（flag 仅 dev 开）由 gstack /qa 人工/截图验证，向用户说明。
**关键用户路径**：
1. admin 发布 plain 公告 → 用户铃铛出现红点 → 打开通知中心看到 → 点开 → 红点减少。
2. admin 发布 survey（含四题型）→ 用户打开问卷 → 作答 → 提交 → 再次打开显示"已提交"只读态。
3. admin 统计页：已读率正确、已读/未读用户列表正确、survey 聚合（选项计数/评分分布/文本列表）正确。
4. feature flag off：用户端无铃铛、后端端点返回 ErrFeatureDisabled。

## 风险/回退
- 后端 AutoMigrate 注册点找不到 → Pause（但已知项目用 AutoMigrate，grep 可定位）。
- 前端铃铛全局 layout 定位不明 → 实现者读 layout 组件确认，必要时退化为 Sidebar 菜单项 + `/notifications` 路由。
- 任一 task review 出 P0 → 修复重审，不进下一 task。
