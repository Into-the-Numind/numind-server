## Prod ServiceEdit bug 调查报告 (2026-04-30)

**调查员**: Agent C-prod (只读，无任何写操作)
**调查时间**: 2026-04-30

---

### Phase 1: prod 运行版本

**prod 容器情况：**

| 容器 | 镜像 | 创建时间 |
|------|------|---------|
| `numind-admin-server-prod` | `pmtmyaggy/numind-admin:admin-v1.4.1` | 2026-04-19T22:50:08Z |
| `numind-admin-web-prod` | `pmtmyaggy/numind-admin-web:v1.4.0` | 10 天前（~2026-04-20） |

**prod admin server 构建的 git 版本**：`admin-v1.4.1` tag

- `admin-v1.4.1` 包含的最新 commit（HEAD）：`c16fb88 fix(billing): use effective legacy check instead of billing_mode field only`
- `admin-v1.4.1` 的 tag 创建日期：2026-04-19T22:43:58Z（Docker image 同一时刻 build）

**三个关键修复 commit 是否包含在 admin-v1.4.1 中：**

| commit | 描述 | 包含于 admin-v1.4.1？ |
|--------|------|---------------------|
| `2c20398` (2026-04-18 20:36) | fix(admin): CreateRoute honors is_active=false from request | ✅ 是 |
| `9a4199d` (2026-04-18 20:44) | fix(admin): CreateProvider honors is_active=false | ✅ 是 |
| `376486c` (2026-04-19 00:00) | fix(store): sweep GORM default:true bool Create regressions | ✅ 是 |
| `fccedb2` (2026-04-30 19:05) | fix(gorm): regression test — db.Save() correctly handles default:true bool on Update path | ❌ 否（今天才提交，prod 未部署） |

**结论**：prod 已包含 Create 路径的 3 个 GORM bool 修复。`fccedb2` 是今天（2026-04-30）刚加的 Update 路径回归测试，尚未发布到 prod——但这只是测试代码，不影响功能逻辑。

**prod 实际运行的 UpdateService 逻辑**（store.go:190-216）：
```go
func (s *gormStore) SaveService(ctx context.Context, svc *model.AIService) error {
    if svc.ID == 0 {
        // Create 路径：有 fixup 逻辑（wantActive 保存 + UpdateColumn）
        ...
    } else {
        // Update 路径：直接 db.Save(svc)
        if err := s.db.WithContext(ctx).Save(svc).Error; err != nil { ... }
    }
}
```

Update 路径直接使用 `db.Save(svc)`。根据 GORM v2 的实现（finisher_api.go `SELECT "*"`），Save 对已有 ID 的 struct 会显式 SELECT 所有字段进 UPDATE，包括 `is_active=false`。代码层面逻辑是正确的。

---

### Phase 2: 实际请求流分析

**日志检索结果**：扫描了 `numind-admin-server-prod` 容器日志中所有 `is_active` 相关条目（共 217 条匹配）。

**关键发现：prod 数据库 audit log 中从未出现过 `is_active=false` 的 service.update 记录。**

所有历史 UPDATE 语句（从 GORM 日志抓取）如下（全部 `is_active=true`）：
```sql
UPDATE `ai_service` SET ... `is_active`=true ... WHERE `id` = 16  -- 2026-04-28
UPDATE `ai_service` SET ... `is_active`=true ... WHERE `id` = 12  -- 2026-04-28
UPDATE `ai_service` SET ... `is_active`=true ... WHERE `id` = 14  -- 2026-04-28
UPDATE `ai_service` SET ... `is_active`=true ... WHERE `id` = 5   -- 2026-04-28
UPDATE `ai_service` SET ... `is_active`=true ... WHERE `id` = 22  -- 2026-04-28
...（全部 is_active=true）
```

**Audit log 最新 10 条**（全部来自 2026-04-28，最新 service.update）：
```json
{"after": {"is_active": true, ...}, "before": {"is_active": true, ...}}
```

**prod 数据库当前所有 ai_service 记录**（16 条，全部 `is_active=1`）：

| id | model_key | is_active |
|----|-----------|-----------|
| 1~25 | (全部 16 条) | 1 (全部激活) |

**结论**：没有任何证据显示用户曾经成功或失败地尝试将某条 service 设为 `is_active=false`。可能是：
1. 用户报告时尚未实际操作（或操作的是 dev 环境？）
2. 日志仅保留近期记录，更早的操作已轮转（需确认 docker 日志 --since 覆盖范围）

---

### Phase 3: schema 对比

**prod is_active 列定义**（来自 `SHOW COLUMNS FROM ai_service`）：
```
Field     Type         Null  Key  Default  Extra
is_active tinyint(1)   YES        1
```

**dev/本地代码 model 定义**（`model.AIService.IsActive`）：
```go
IsActive bool `gorm:"not null;default:true" json:"is_active"`
```

**差异分析**：
- prod DB 列：`Null=YES`，`Default=1`（允许 NULL，但 DEFAULT=1）
- 代码 tag：`not null;default:true`

这里存在一个轻微的 schema 漂移：prod 列允许 NULL（`Null=YES`），而代码标注为 `not null`。这不会导致 is_active=false 写入失败，但说明 migration 执行不完全或列定义后被修改过。

**结论 3**：schema 差异存在但不是 is_active=false 写不进去的直接原因。`tinyint(1) DEFAULT 1` 完全可以存储 0（false）。

---

### Phase 4: 缓存/前端分析

**后端 Registry 缓存**：in-process 内存 `map[uint64]*serviceCacheEntry`（非 Redis）。`SaveService` 完成后立即调用 `r.cache.InvalidateService(svc.ID)`，不存在读出陈旧缓存的问题。

**Redis 缓存**：Redis 仅用于 SOP 路由/billing，不用于 ai_service admin CRUD 的读取路径。无 Redis 缓存陈旧问题。

**前端 ServiceEdit.vue 分析**：

`buildPayloadBase()` 函数（第 387-407 行）完整传送 `is_active: form.value.is_active`：
```typescript
function buildPayloadBase() {
  return {
    ...
    is_active: form.value.is_active,  // ← 完整传送，不会漏掉
  };
}
```

`UpdateServiceRequest` 定义为 `Partial<CreateServiceRequest>`，`is_active` 类型为 `boolean?`。字段本身是可选的，但当 checkbox 是 false 时，JavaScript 布尔值 `false` 会被正常序列化进 JSON（`{"is_active": false, ...}`），不存在漏传问题。

**控制器 updateServiceReq**（ai_service.go:244-258）：
```go
type updateServiceReq struct {
    ...
    IsActive *bool `json:"is_active"`
}
```
使用 `*bool` 指针，JSON `false` 可以正常反序列化为 `&false`，然后 `if req.IsActive != nil { svc.IsActive = *req.IsActive }` 会正确设置为 false。

**prod admin-web 版本** `v1.4.0` 包含的 ServiceEdit.vue 已有完整的 `buildPayloadBase` 逻辑（按 git 历史确认，a1015fd 后就已经存在）。

**结论 4**：前端和后端逻辑链路在 admin-v1.4.1 / admin-web-v1.4.0 这两个版本中是完整且正确的。

---

### Phase 5: 推断根因

**核心发现：prod 从未有过 is_active=false 的实际 UPDATE 尝试（audit log + SQL log 均无记录）。**

按可能性排序：

**1. 最可能：用户报告的 bug 未在 prod 实际发生（或用户是在 dev 环境观察到的）**

prod 数据库 audit log 和 GORM SQL 日志均无任何 `is_active=false` 的 service.update 操作痕迹。所有 16 条 ai_service 记录均为 `is_active=1`。如果用户真的保存过 is_active=false，audit log 中必然存在记录（diff_json 会显示 before/after 差异）。

可能的实际情况：
- 用户在 dev 环境（49.233.219.254:9104）测试了此功能，而 dev 环境可能运行的是更旧的 build
- 用户观察到的是 UI checkbox 视觉 bug（勾选状态不保持），而非 DB 数据问题
- 用户误操作或报告时混淆了环境

**2. 次可能：如果 bug 确实发生在 prod，原因是 prod admin-web v1.4.0 与 admin-server v1.4.1 的 admin-web 是旧版本（10 天前，2026-04-20 部署）**

Admin-web `v1.4.0` 的 ServiceEdit.vue 和 buildPayloadBase 函数已经存在，不太可能是老版本问题。但如果用户使用了浏览器缓存的更旧的 JS bundle（在 v1.4.0 部署前访问过该页面），可能有 checkbox 绑定问题。**建议用户 Ctrl+Shift+R 强制刷新后重现。**

**3. 最不可能：GORM Update 路径 bug**

db.Save(svc) 在 GORM v2 对 Update 路径是安全的（finisher_api.go SELECT "*"），且 prod 日志中所有 UPDATE SQL 都包含了完整字段（is_active 字段在每个 UPDATE 语句中均有出现）。Update 路径无 GORM default:true 零值跳过问题。

---

### 推荐下一步

1. **最优先：请用户明确复现路径** — 具体是哪个 service ID，在什么时间（date），是否是 prod 还是 dev，是否清除了浏览器缓存后仍然复现
2. **确认 dev 环境版本** — SSH 到 dev (49.233.219.254) 检查 numind-admin-server 运行的是哪个版本，是否包含 ai-service CRUD 相关修复
3. **不建议** 贸然给 prod 部署新版本 — 当前 prod 逻辑链路分析是正确的，没有确认的 is_active=false 写入失败案例，冒险重新部署风险大于收益
4. **不建议** 直接 UPDATE DB 修复 — 目前 prod 所有服务 is_active 均为 1，没有错误数据需要修复

---

*调查状态: DONE（仅只读，无任何写操作）*
