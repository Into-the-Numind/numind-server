# 机构自定义品牌名（org-branding）— 技术设计 Spec

> 输入：S1 提案 §4 PRD。本 spec 锁定 API 契约供 3 仓库并行实现（Tier 2）。
> 设计方法：产品形态已由 4 项 AskUserQuestion + S1 PRD 锁定（brainstorming 前置完成），本阶段聚焦契约与边界。

## 1. 数据模型

### 1.1 User 模型新增字段（`internal/pkg/model/user.go`）
```go
// 机构品牌名：仅父账户（ParentUserID==nil）有意义，子账户继承父账户的值。
// 空串表示未设置，展示层兜底"有数AI"。
CompanyName string `gorm:"size:100;not null;default:''" json:"company_name"`
```
- **确切位置**：放在 `AvatarURL`（行 15）之后、`ParentUserID`（行 18）之前。
- AutoMigrate 会自动建列（dev 重启即生效）；同时提供 migration SQL 作 prod 权威。

### 1.2 Migration（`migrations/20260616_000000_add_user_company_name.sql`）
```sql
-- org-branding: User 表新增机构品牌名字段
-- 仅父账户有意义，子账户继承；空串=未设置（展示层兜底"有数AI"）
-- MySQL 8.0 不支持 ADD COLUMN IF NOT EXISTS，用 information_schema 守卫保证幂等
SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'user' AND COLUMN_NAME = 'company_name');
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `user` ADD COLUMN `company_name` VARCHAR(100) NOT NULL DEFAULT '''' AFTER `avatar_url`',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;
```

## 2. 后端 API 契约

### 2.1 `PUT /v1/users/me`（扩展，不新建端点）
**请求** `UpdateUserProfileRequest`（`pkg/api/numind/v1/user.go`）新增：
```go
CompanyName *string `json:"company_name" valid:"stringlength(0|100)"`
```
- `*string` 区分"未传"(nil，不改)vs"传空串"(清空名称回兜底)。
- 长度 0~100；前端 trim 后提交。

**biz `UpdateUserProfile`**（`biz/user/user.go:187`）新增逻辑：
```go
if req.CompanyName != nil {
    if user.ParentUserID == nil {        // 仅父账户可写
        user.CompanyName = strings.TrimSpace(*req.CompanyName)
    }
    // 子账户：静默忽略 company_name（继承语义，不报错）
}
```

**store `UpdateUser`**（`store/user.go:104`）改用 **map 形式**（[[database]] §6b 钦定：`Updates(struct)` 会跳过零值，map 形式总是包含 key，空串才能落库 → 保证 AC3 清空生效）：
```go
func (u *users) UpdateUser(ctx context.Context, user *model.User) error {
    // map 形式：显式列出可更新列（保留"避免意外更新敏感字段"的初衷），
    // 且 map 总是写入 key，company_name="" 也能持久化（清空名称）。updated_at 由 GORM map 模式自动刷新。
    return u.db.Model(user).Updates(map[string]interface{}{
        "nickname":     user.Nickname,
        "avatar_url":   user.AvatarURL,
        "company_name": user.CompanyName,
    }).Error
}
```
- ⚠️ 不可沿用旧的 `Select(...).Updates(user)` struct 形式——按 §6b，struct 形式会静默丢弃 `company_name=""`，AC3 会在生产挂掉。
- 其它调用方（avatar 路径 `UpdateUserAvatar`）传入的 user 是先 load 的完整对象，company_name=当前值，写回是 no-op，安全。

### 2.2 `GET /v1/users/me`（扩展响应）
**controller `GetCurrentUser`**（`controller/v1/user/get.go:34`）在 response gin.H 加：
```go
brandName, _ := ctrl.b.Users().ResolveCompanyName(c, userWithStats)
response["company_name"] = brandName   // 已解析的有效品牌名（父用自己/子用父账户/空串=未设置）
```
- 返回**原始有效值**（可能为空串），不在后端兜底"有数AI"——保留"已设/未设"区分，兜底交前端展示层。

**新增 biz 方法 `ResolveCompanyName`**（`biz/user/user.go`，业务逻辑归 biz）：
```go
// ResolveCompanyName 返回用户的有效机构品牌名：
// 父账户(ParentUserID==nil)用自己的 CompanyName；子账户用父账户的 CompanyName。
// 均可能为空串（未设置）。
func (b *userBiz) ResolveCompanyName(ctx context.Context, user *model.User) (string, error) {
    if user.ParentUserID == nil {
        return user.CompanyName, nil
    }
    parent, err := b.ds.Users().GetUserByID(ctx, *user.ParentUserID)
    if err != nil {
        if errors.Is(err, gorm.ErrRecordNotFound) {
            return "", nil   // 父账户异常缺失 → 兜底空串（前端显示"有数AI"），不阻断 /me
        }
        return "", err
    }
    return parent.CompanyName, nil
}
```
- 子账户多一次单行查（非列表接口，无 N+1）。
- **必须**在 `UserBiz` interface（`biz/user/user.go` 行 ~27-49）声明方法签名，否则编译期 `var _ UserBiz = (*userBiz)(nil)` 检查 + controller 经 interface 调用会编译失败：
  ```go
  ResolveCompanyName(ctx context.Context, user *model.User) (string, error)
  ```

## 3. 前端契约 — numind-web-v3

### 3.1 store（`src/stores/user.ts`）
- `UserInfo` 接口加 `company_name?: string`。
- 新增 computed：
```ts
const displayBrandName = computed(() => (userInfo.value?.company_name || '').trim() || '有数AI')
```
- 已有 `isParentUser` 复用，决定设置页是否显示编辑框。

### 3.2 侧边栏（`src/components/layout/AppSidebar.vue:4-6`）
- 当前硬编码 `靓 / 靓靓·海外IP研究所` → 改读 `userStore.displayBrandName`。
- `logo-text` 展示 `displayBrandName`；`logo-mark`（折叠态首字）取 `displayBrandName[0]`。
- 兜底：store 无数据时 computed 已返回"有数AI"，天然安全。

### 3.3 设置页（`src/views/SettingsView.vue`）
- **仅当 `isParentUser` 为 true** 渲染"公司名称"编辑项（输入框 + 保存按钮）。
- 校验：blur 时触发（ui-ux 硬规则 §3），长度 ≤100，trim。
- 保存：调 `updateProfile({ company_name })`（`src/api/...` 现有 PUT /v1/users/me 封装，加字段）；成功后 `userStore.fetchUserInfo()` 重拉刷新侧边栏 + toast；失败读 message。
- 状态：保存中 loading；空数据/错误按现有页风格。

### 3.4 登录页（`src/views/LoginView.vue:181-189`）
- 移除 COS 图片 background-image，改为"有数AI"文字标题（沿用现有 logo 区位置/尺寸，最小改动，见 [[feedback_bp_minimal_diff]]）。

## 4. 前端契约 — numind-admin-web
### 4.1 登录页（`src/views/LoginView.vue:39-41`）
- `<div class="login-logo">N</div>` → "有数AI" 文字（调整 CSS 让文字适配，宽度/字号自适应）。
- **仅此一处改动**，管理端不加公司名设置入口。

## 5. 权限 / 边界
- 编辑：仅 `ParentUserID==nil`（父账户）。子账户后端守卫 + 前端隐藏（双层）。
- 空串 / 全空格：trim 后空 → 视为未设置 → 展示"有数AI"。
- 超长（>100）：valid tag 拒绝，前端同步限制。
- 父改名 → 子账户下次拉 /me 生效（无需实时推送）。
- 父账户记录异常缺失：ResolveCompanyName 返回空串，/me 不报错。

## 6. 测试拓扑
- **后端单测**（biz 层，mock store）：
  - `TestUpdateUserProfile_ParentSetsCompanyName`：父账户设名 → 持久化。
  - `TestUpdateUserProfile_ChildCompanyNameIgnored`：子账户传 company_name → 不写入（继承守卫）。
  - `TestUpdateUserProfile_ClearCompanyName`：父账户传空串 → 清空。
  - `TestResolveCompanyName_Parent`：父账户返回自己的名。
  - `TestResolveCompanyName_Child`：子账户返回父账户的名。
  - `TestResolveCompanyName_ChildParentMissing`：父缺失返回空串不报错。
- **后端 store 层集成测试**（in-memory SQLite，AutoMigrate 建列，仿 [[database]] §6b 的 `TestStore_SaveService_UpdateIsActiveFalse` 模式）：
  - `TestStore_UpdateUser_ClearCompanyName`：先写非空 company_name → 再写空串 → DB 行 company_name=""。
    **关键**：biz mock 测不出 GORM 零值落库行为，必须有这个真打 SQLite 的 store 测才能守住 AC3 不回归。
- **前端**：S5 用 gstack /qa 走核心路径（设名→侧边栏、子账户继承、兜底、登录页文字）。回归保护以后端单测为主（展示型前端不强制 E2E）。

## 7. AI 可观测性
- 不涉及 LLM 调用 → 无 trace topology。N/A。

## 8. 仓库并行策略（S4 预告）
- Tier 2：后端契约锁定后，server / web-v3 / admin-web 可跨仓库并行实现。
- admin-web 改动极小（1 文件），可与 web-v3 并行或顺带。
