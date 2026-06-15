# 机构自定义品牌名（org-branding）— 实施规划 Plan

> 输入：S2 spec。后端 task 排在前端之前；API 契约已在 spec 锁定。
> 每个 task 走 TDD（RED→GREEN→REFACTOR），完成后并行双 reviewer（规则 6）。

## 任务依赖图
```
T1 (server 后端全链路) ──┬──> T2 (web-v3 前端)
                         └──> （契约锁定，T2 可按契约并行；集成在 T1 完成后）
T3 (admin-web 登录页)  ── 独立，无依赖
T4 (S5 验证策略)       ── 文档，最后
```
- T1 与 T3 跨仓库 disjoint → Tier 2 可并行。
- T2 依赖 T1 的 API 契约（spec 已锁），可 Tier 2 并行实现、T1 完成后联调。
- 实操（单 session autopilot）：先做 T1（后端），再并行 T2 + T3（web-v3 / admin-web 跨仓库 disjoint，Tier 2）。

---

## T1 — 后端 company_name 全链路（numind-server）

**描述**：在后端打通 company_name 的存储、更新（父账户守卫）、与 GET /me 的有效品牌名解析（父子继承）。

**涉及文件**：
- `internal/pkg/model/user.go` — User 加 `CompanyName string`（AvatarURL 后 / ParentUserID 前）。
- `migrations/20260616_000000_add_user_company_name.sql` — 幂等 ADD COLUMN（information_schema 守卫）。
- `pkg/api/numind/v1/user.go` — `UpdateUserProfileRequest` 加 `CompanyName *string valid:"stringlength(0|100)"`。
- `internal/numind/biz/user/user.go` — UpdateUserProfile 加父账户守卫；新增 `ResolveCompanyName`；`UserBiz` interface 加方法签名。
- `internal/numind/store/user.go` — `UpdateUser` 改 map 形式（含 company_name）。
- `internal/numind/controller/v1/user/get.go` — GET /me response 加 `company_name`（调 ResolveCompanyName）。
- 测试：`biz/user/*_test.go`（6 个 biz 单测，mock store）+ `store/user_test.go`（`TestStore_UpdateUser_ClearCompanyName`，in-memory SQLite）。

**TDD 顺序**：先写 6 biz 测 + 1 store 测（RED）→ 实现 → GREEN → 重构。

**验收条件**：
- `go test ./internal/numind/biz/user/... ./internal/numind/store/...` 全过（含清空场景）。
- `task lint` 退出 0。
- `GET /v1/users/me` 手动/测试确认含 `company_name` 字段。
- 父账户 PUT company_name 落库；子账户 PUT company_name 被忽略；父账户传空串清空。

---

## T2 — 前端品牌名展示 + 编辑（numind-web-v3）

**描述**：左上角侧边栏改为动态品牌名（兜底"有数AI"）；父账户设置页可编辑公司名；登录页 logo 换"有数AI"文字。

**涉及文件**：
- `src/stores/user.ts` — `UserInfo` 加 `company_name?`；新增 `displayBrandName` computed（兜底"有数AI"）。
- `src/components/layout/AppSidebar.vue` — logo-text/logo-mark 读 `displayBrandName`（移除硬编码"靓靓·海外IP研究所"）。
- `src/views/SettingsView.vue` — 仅 `isParentUser` 时显示"公司名称"编辑项（输入框 blur 校验 + 保存 → PUT /me → 重拉 /me + toast）。
- `src/api/auth.ts` — **新增** `updateProfile` PUT 封装（现仅有 `getUserInfo` GET，无 PUT /me 封装；与"设置页只读"现状一致）。签名：`updateProfile(data: { nickname?: string; avatar_url?: string; company_name?: string }) => request.put('/v1/users/me', data)`。
- `src/views/LoginView.vue` — 移除 COS 图片，改"有数AI"文字标题（最小改动沿用排版）。

**验收条件**：
- `npm run lint && npm run type-check` 退出 0（scope 改动文件）。
- 父账户设名 → 侧边栏更新；子账户登录显示父名且无编辑框；未设置显示"有数AI"；登录页显示"有数AI"文字。

---

## T3 — 管理端登录页 logo 文字（numind-admin-web）

**描述**：管理端登录页 `N` 方块 → "有数AI" 文字。仅此一处，不加公司名设置入口。

**涉及文件**：
- `src/views/LoginView.vue` — `<div class="login-logo">N</div>` → "有数AI"；调 CSS 适配文字宽度/字号。

**验收条件**：
- `npm run lint && npm run type-check` 退出 0。
- 管理端登录页显示"有数AI"文字。

---

## T4 — S5 验证策略（规则 10，S3 必产）

**验证方式**：**gstack /qa 浏览器截图 QA**（本地 localhost:5173 + admin 本地）为主 + 后端 `go test` 回归。

**理由**：
- 本功能核心是**展示型**（品牌名渲染 + 登录页文字），最适合浏览器视觉验证（截图比对）。
- 真正需要持久回归保护的是**后端父子继承 + 清空语义**（高风险点：B2B2C 解析 + GORM 零值），已由 T1 的 biz 单测 + store SQLite 测覆盖（永久回归保护）。
- 前端为纯展示，不强制 Playwright E2E（gstack /qa 一次性验证即可），符合 testing.md：展示型前端用 /qa，高风险业务逻辑用持久化测试——本功能高风险部分在后端且已有持久测试。
- **诚实声明**（规则 10）：gstack /qa 不产生持久化前端测试，前端未来改动无自动回归保护；但前端是展示层、回归风险低，且后端继承/清空逻辑有 Go 测兜底。本功能不涉及支付/会员/权限等必须 E2E 的高风险业务。

**S5 需验证的关键用户路径**：
1. 父账户登录 → 设置页填"测试公司A"保存 → 侧边栏左上角显示"测试公司A"。
2. 父账户清空公司名保存 → 侧边栏回退显示"有数AI"。
3. 子账户登录（父已设"测试公司A"）→ 侧边栏显示"测试公司A" 且设置页无公司名编辑框。
4. 未设置公司名的账户 → 侧边栏显示"有数AI"。
5. 用户端登录页 → 显示"有数AI"文字 logo（非莫小派图片）。
6. 管理端登录页 → 显示"有数AI"文字（非 N 方块）。

---

## 验证策略审查点（S3 gate 一并审）
- 高风险（B2B2C 父子继承 + GORM 清空零值）是否有持久化测试？→ 是（T1 biz + store 测）。
- 展示型前端选 /qa 是否合理？→ 是（非支付/权限/会员等级，回归风险低）。
