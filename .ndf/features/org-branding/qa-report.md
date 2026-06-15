# QA Report — 机构自定义品牌名（org-branding）

## 验证环境（S5 本地）
- 后端：单元/集成测试（in-memory SQLite，真实 store）；运行态后端逻辑由 Go 测覆盖
- 前端：本地 vite dev（web-v3 :5173 / admin-web :5174）
- 浏览器：gstack browse（headless Chromium）

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go build | `go build ./...` | PASS | exit 0（仅 sqlite-vec cgo 警告，无关）|
| Go test（biz/user）| `go test ./internal/numind/biz/user/...` | PASS | 8 测全过（父设/子忽略/清空/继承/父缺失/nil 不改）|
| Go test（store 清空守卫）| `go test -run TestStore_UpdateUser_ClearCompanyName` | PASS | GORM map 形式零值落库 |
| Go test（相邻包）| customer biz/store/membership | PASS | 加字段未破坏既有断言 |
| Go vet | `go vet ./...`（含 salesrag mock）| PASS | 无 UserBiz mock 破坏 |
| golangci-lint（改动包）| `golangci-lint run` | PASS | exit 0 |
| Vue build (web-v3) | `npm run build` | PASS | exit 0（仅既有 chunk 警告）|
| Vue type-check (web-v3) | `npm run type-check` | PASS | exit 0 |
| Vue lint (web-v3) | `npx eslint <改动文件>` | PASS | exit 0 |
| Admin build | `npm run build` | PASS | exit 0 |
| Admin type-check | `npm run type-check` | PASS | exit 0 |
| Admin lint | `npx eslint LoginView.vue` | PASS | exit 0 |
| E2E | `npm run test:e2e` | N/A | 验证策略选 gstack /qa（展示型前端，见 plan T4）|

## 浏览器 QA（本地）
- web-v3 登录页（localhost:5173/login）：`.login-logo` 文本 = "有数AI"，品牌绿 wordmark 渲染正常，无 console error。截图 `/tmp/org_login_v3.png`。**AC7 PASS**。
- admin-web 登录页（localhost:5174/login）：`.login-logo` = "有数AI"（pill 自适应宽度容纳 4 字），`.login-title` = "有数AI管理后台"，无 console error。截图 `/tmp/org_login_admin.png`。**AC8 PASS**。

## 可观测性验证
- N/A（不涉及 AI/LLM 调用）

## PRD 验收标准核对

| 验收标准 | S5 结果 | 验证方式 / 备注 |
|---|---|---|
| AC1 父账户设名→侧边栏显示 | 逻辑 PASS / 视觉待 S6 | 后端 biz 测 PASS；侧边栏动态绑定 type-check+review PASS；端到端视觉需部署后端（S6 dev）|
| AC2 子账户继承父名 | 逻辑 PASS / 视觉待 S6 | 后端 ResolveCompanyName 测 PASS（子返回父名）；前端读 company_name 直显，无前端回查 |
| AC3 清空→兜底"有数AI" | 逻辑 PASS / 视觉待 S6 | 后端 store SQLite 清空测 PASS；前端 displayBrandName 兜底逻辑 review PASS |
| AC4 未设置→"有数AI" | PASS | displayBrandName computed 兜底；登录页本身即兜底文字 |
| AC5 子账户无编辑框 | 逻辑 PASS / 视觉待 S6 | `v-if="isParentUser"` 守卫 review PASS；视觉需登录态（S6 dev）|
| AC6 子账户调 PUT 被忽略 | PASS | 后端 biz 守卫测 TestUpdateUserProfile_ChildCompanyNameIgnored PASS |
| AC7 用户端登录页文字 | PASS | 本地 gstack 视觉确认 |
| AC8 管理端登录页文字 | PASS | 本地 gstack 视觉确认 |
| AC9 GET /me 含 company_name | PASS | controller 注入 + biz 解析，build/test PASS |

## 结论
ALL_PASS（本地可验证范围）

**诚实声明（NDF 规则 10）**：认证态视图（侧边栏品牌名随机构变化、设置页编辑、子账户继承）的**端到端视觉**验证需要部署后的后端 + DB + 登录态，本地无完整后端栈（MySQL/Redis）。这部分在 **S6 dev 部署后用真实账号 gstack /qa 全面验收**（届时可实际设置公司名→观察侧边栏与子账户传播）。后端继承/清空等高风险逻辑已有**持久化 Go 测**回归保护，不依赖一次性浏览器验证。

## 失败项修复要求
无。
