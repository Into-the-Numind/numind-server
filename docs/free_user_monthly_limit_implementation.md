# 免费用户月度限制功能实现总结

## 功能概述

实现了免费用户的月度卡册创建限制功能，免费用户每月可免费使用5本额度，每月1号0点自动重置。

## 实现的功能

### 1. 数据库字段扩展

**文件**: `internal/pkg/model/user.go`

新增字段：
```go
type User struct {
    // ... 其他字段
    FreeUserMonthlyBookCount  int        `gorm:"default:0" json:"free_user_monthly_book_count"`         // 免费用户本月创建的卡册数量
    FreeUserLastResetDate     *time.Time `gorm:"index" json:"free_user_last_reset_date"`                // 免费用户上次重置时间
}
```

### 2. 免费用户月度计算逻辑

#### 核心方法
- `IsInNewFreeUserMonth()`: 检查免费用户是否进入新的日历月
- `GetCurrentFreeUserMonthStart()`: 获取免费用户当前月的开始时间（每月1号0点）
- `GetCurrentFreeUserMonthEnd()`: 获取免费用户当前月的结束时间（下月1号0点）

#### 计算规则
- 免费用户月 = 日历月（每月1号0点重置）
- 基于 `FreeUserLastResetDate` 字段判断是否需要重置
- 每月1号0点自动重置额度

### 3. 免费用户月度限制检查

#### 限制规则
- **免费用户** (`free`): 每月最多5本卡册
- **订阅会员** (`subscription`): 每月最多100本卡册（原有功能）
- **both类型** (`both`): 每月最多100本卡册（原有功能）
- **资源包用户** (`package`): 无月度限制（原有功能）

#### 核心方法
- `CanCreateBookAsFreeUser()`: 检查免费用户是否可以创建卡册（月度限制）
- `GetRemainingFreeUserMonthlyBooks()`: 获取免费用户当前月内剩余可创建的卡册数量

### 4. 自动重置机制

#### 重置触发
- 在 `calculateCreatePermission()` 方法中检查
- 当检测到免费用户进入新日历月时自动重置

#### 重置逻辑
```go
// 检查免费用户是否需要重置月度计数
if user.MembershipType == model.MembershipTypeFree && user.IsInNewFreeUserMonth() {
    // 重置免费用户月度计数
    if err := mc.resetFreeUserMonthlyBookCount(user); err != nil {
        log.C(context.Background()).Errorw("Failed to reset free user monthly book count", "user_id", user.ID, "error", err.Error())
    }
}
```

### 5. 卡册创建计数

**文件**: `internal/numind/biz/book/async_processor.go`

在 `CreateBookAsync` 方法中增加免费用户月度计数：
```go
// 增加免费用户月度卡册计数（仅对免费用户）
if err := p.biz.Users().IncrementFreeUserMonthlyBookCount(ctx, userID); err != nil {
    log.C(ctx).Errorw("Failed to increment free user monthly book count", "error", err.Error())
}
```

### 6. 权限检查更新

**文件**: `internal/numind/controller/v1/membership/membership.go`

更新创建权限逻辑：
```go
// 检查免费用户限制
if user.MembershipType == model.MembershipTypeFree {
    // 检查免费用户月度限制
    if !user.CanCreateBookAsFreeUser() {
        remaining := user.GetRemainingFreeUserMonthlyBooks()
        return &CreatePermission{
            CanCreate: false,
            Reason:    fmt.Sprintf("免费用户本月已创建%d个卡册，达到月度限制5个，剩余%d个，下月1号重置", user.FreeUserMonthlyBookCount, remaining),
        }
    }

    // 免费用户月度限制未达到，可以创建
    remaining := user.GetRemainingFreeUserMonthlyBooks()
    return &CreatePermission{
        CanCreate: true,
        Reason:    fmt.Sprintf("免费用户，本月已创建%d个卡册，还可以创建%d个", user.FreeUserMonthlyBookCount, remaining),
    }
}
```

### 7. 数据库操作方法

**文件**: `internal/numind/biz/user/user.go`

新增方法：
```go
// ResetFreeUserMonthlyBookCount 重置免费用户月度卡册计数
func (b *userBiz) ResetFreeUserMonthlyBookCount(ctx context.Context, userID uint) error

// IncrementFreeUserMonthlyBookCount 增加免费用户月度卡册计数
func (b *userBiz) IncrementFreeUserMonthlyBookCount(ctx context.Context, userID uint) error
```

## 使用场景

### 场景1：免费用户创建卡册
1. 免费用户尝试创建卡册
2. 系统检查 `CanCreateBookAsFreeUser()`
3. 如果未达到月度限制（5本），允许创建并增加计数
4. 如果达到限制，拒绝创建并提示剩余数量

### 场景2：月度重置
1. 免费用户进入新的日历月（每月1号0点）
2. 系统检测到 `IsInNewFreeUserMonth()` 返回 true
3. 自动重置 `FreeUserMonthlyBookCount` 为 0
4. 用户重新获得5本卡册的创建权限

### 场景3：权限检查
1. 用户尝试创建卡册时，系统会检查用户类型
2. 免费用户：检查月度5本限制
3. 订阅会员：检查月度30本限制（原有功能）
4. 资源包用户：无月度限制（原有功能）

## API 响应示例

### 免费用户权限检查
```json
{
  "code": 0,
  "data": {
    "can_create": true,
    "reason": "免费用户，本月已创建3个卡册，还可以创建2个",
    "membership_type": "free",
    "is_pro": false,
    "free_user_monthly_book_count": 3
  }
}
```

### 达到限制时的响应
```json
{
  "code": 0,
  "data": {
    "can_create": false,
    "reason": "免费用户本月已创建5个卡册，达到月度限制5个，剩余0个，下月1号重置",
    "membership_type": "free",
    "is_pro": false,
    "free_user_monthly_book_count": 5
  }
}
```

## 数据库迁移

### 新增字段
```sql
-- 添加免费用户月度卡册计数字段
ALTER TABLE user 
ADD COLUMN free_user_monthly_book_count INT DEFAULT 0 COMMENT '免费用户本月创建的卡册数量';

-- 添加免费用户上次重置时间字段
ALTER TABLE user 
ADD COLUMN free_user_last_reset_date TIMESTAMP NULL COMMENT '免费用户上次重置时间';

-- 添加索引以提高查询性能
ALTER TABLE user ADD INDEX idx_free_user_last_reset_date (free_user_last_reset_date);

-- 为现有免费用户初始化字段
UPDATE user 
SET free_user_monthly_book_count = 0,
    free_user_last_reset_date = NOW()
WHERE membership_type = 'free' 
AND (free_user_monthly_book_count IS NULL OR free_user_last_reset_date IS NULL);
```

## 配置参数

### 可配置的限制参数
```go
const (
    FreeUserMonthlyBookLimit = 5  // 免费用户每月最大卡册数量
    SubscriptionMonthlyBookLimit = 100  // 订阅会员每月最大卡册数量
)
```

## 监控和日志

### 关键日志
- 免费用户月度计数重置：`Free user monthly book count reset`
- 免费用户月度计数增加：`Failed to increment free user monthly book count`
- 权限检查：`Failed to reset free user monthly book count`

### 监控指标
- 免费用户月度卡册创建数量分布
- 达到限制的免费用户数量
- 月度重置频率

## 总结

免费用户月度限制功能已完全实现，支持：

1. ✅ 免费用户月度5本卡册限制
2. ✅ 基于日历月的自动重置机制（每月1号0点）
3. ✅ 智能权限检查
4. ✅ 完整的API支持
5. ✅ 详细的用户反馈
6. ✅ 向后兼容性

用户现在可以享受更合理的免费额度管理，系统也能更好地控制资源使用。免费用户每月有5本卡册的免费额度，每月1号0点自动重置，超过限制后需要升级会员或购买资源包。
