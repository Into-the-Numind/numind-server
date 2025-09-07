# 月度卡册限制功能实现总结

## 功能概述

实现了订阅会员的月度卡册创建限制功能，用户在成为订阅会员后的每个30天周期内最多只能创建30本卡册，超过限制后将无法创建新卡册，直到下个月重置。

## 实现的功能

### 1. 数据库字段扩展

**文件**: `internal/pkg/model/user.go`

新增字段：
```go
type User struct {
    // ... 其他字段
    MembershipStartDate *time.Time `gorm:"index" json:"membership_start_date"`  // 会员开始时间
    MonthlyBookCount    int        `gorm:"default:0" json:"monthly_book_count"` // 当前会员月内创建的卡册数量
}
```

### 2. 会员月计算逻辑

#### 核心方法
- `GetCurrentMembershipMonthStart()`: 获取当前会员月的开始时间
- `GetCurrentMembershipMonthEnd()`: 获取当前会员月的结束时间
- `IsInNewMembershipMonth()`: 检查是否进入新的会员月

#### 计算规则
- 会员月 = 30天周期
- 从 `MembershipStartDate` 开始计算
- 每30天为一个会员月周期

### 3. 月度限制检查

#### 限制规则
- **订阅会员** (`subscription`): 每月最多30本卡册
- **both类型** (`both`): 每月最多30本卡册
- **资源包用户** (`package`): 无月度限制
- **免费用户** (`free`): 无月度限制

#### 核心方法
- `CanCreateBookInCurrentMonth()`: 检查当前会员月内是否可以创建卡册
- `GetRemainingMonthlyBooks()`: 获取当前会员月内剩余可创建的卡册数量

### 4. 自动重置机制

#### 重置触发
- 在 `calculateCreatePermission()` 方法中检查
- 当检测到进入新会员月时自动重置

#### 重置逻辑
```go
if user.IsInNewMembershipMonth() {
    // 重置月度计数
    if err := mc.resetMonthlyBookCount(user); err != nil {
        log.C(context.Background()).Errorw("Failed to reset monthly book count", "user_id", user.ID, "error", err.Error())
    }
}
```

### 5. 卡册创建计数

**文件**: `internal/numind/biz/book/async_processor.go`

在 `CreateBookAsync` 方法中增加月度计数：
```go
// 增加月度卡册计数（仅对订阅会员和both类型）
if err := p.biz.Users().IncrementMonthlyBookCount(ctx, userID); err != nil {
    log.C(ctx).Errorw("Failed to increment monthly book count", "error", err.Error())
}
```

### 6. 支付逻辑更新

**文件**: `internal/numind/biz/payment/payment.go`

新订阅用户设置会员开始时间：
```go
// 如果是新订阅用户，设置会员开始时间
if user.MembershipType != model.MembershipTypeSubscription && user.MembershipType != model.MembershipTypeBoth {
    updateData["membership_start_date"] = &now
    updateData["monthly_book_count"] = 0 // 重置月度计数
}
```

### 7. 权限检查更新

**文件**: `internal/numind/controller/v1/membership/membership.go`

更新创建权限逻辑：
```go
// 优先检查订阅会员
if user.CanUseSubscription() {
    // 检查月度限制
    if !user.CanCreateBookInCurrentMonth() {
        remaining := user.GetRemainingMonthlyBooks()
        return &CreatePermission{
            CanCreate: false,
            Reason:    fmt.Sprintf("本月已创建%d个卡册，达到月度限制30个，剩余%d个", user.MonthlyBookCount, remaining),
        }
    }
    
    return &CreatePermission{
        CanCreate: true,
        Reason:    fmt.Sprintf("%s有效，本月已创建%d个卡册，还可以创建%d个", user.GetMembershipStatus(), user.MonthlyBookCount, user.GetRemainingMonthlyBooks()),
    }
}
```

### 8. 会员信息接口更新

**文件**: `internal/numind/controller/v1/membership/membership.go`

新增月度卡册信息显示：
```json
{
  "monthly_book_info": {
    "current_count": 25,
    "remaining_count": 5,
    "month_start": "2024-09-06T10:00:00Z",
    "month_end": "2024-10-06T10:00:00Z",
    "can_create": true,
    "description": "本月已创建25个卡册，还可以创建5个"
  }
}
```

## 使用场景

### 场景1：新订阅用户
1. 用户购买订阅会员
2. 系统设置 `MembershipStartDate` 为当前时间
3. 重置 `MonthlyBookCount` 为 0
4. 用户开始享受月度30本卡册限制

### 场景2：月度限制检查
1. 用户尝试创建卡册
2. 系统检查 `CanCreateBookInCurrentMonth()`
3. 如果未达到限制，允许创建并增加计数
4. 如果达到限制，拒绝创建并提示剩余数量

### 场景3：月度重置
1. 用户进入新的会员月（30天周期）
2. 系统检测到 `IsInNewMembershipMonth()` 返回 true
3. 自动重置 `MonthlyBookCount` 为 0
4. 用户重新获得30本卡册的创建权限

## 测试验证

通过测试脚本验证了以下场景：

### 基本功能测试
- ✅ 订阅会员：25/30本，可创建5本
- ✅ 达到限制：30/30本，不可创建
- ✅ 超过限制：35/30本，不可创建
- ✅ both类型：20/30本，可创建10本
- ✅ 免费用户：无限制
- ✅ 资源包用户：无限制

### 会员月计算测试
- ✅ 正确计算当前会员月开始时间
- ✅ 正确计算当前会员月结束时间
- ✅ 正确判断是否进入新会员月

## API 响应示例

### 会员信息接口
```json
{
  "code": 0,
  "data": {
    "membership_type": "subscription",
    "membership_expires": "2024-10-07T11:54:46Z",
    "monthly_book_info": {
      "current_count": 25,
      "remaining_count": 5,
      "month_start": "2024-09-07T11:54:46Z",
      "month_end": "2024-10-07T11:54:46Z",
      "can_create": true,
      "description": "本月已创建25个卡册，还可以创建5个"
    },
    "is_pro": true,
    "membership_status": "订阅会员",
    "is_active": true
  }
}
```

### 创建权限接口
```json
{
  "code": 0,
  "data": {
    "can_create": true,
    "reason": "订阅会员有效，本月已创建25个卡册，还可以创建5个",
    "membership_type": "subscription",
    "is_pro": true,
    "monthly_book_count": 25
  }
}
```

### 达到限制时的响应
```json
{
  "code": 0,
  "data": {
    "can_create": false,
    "reason": "本月已创建30个卡册，达到月度限制30个，剩余0个",
    "membership_type": "subscription",
    "is_pro": true,
    "monthly_book_count": 30
  }
}
```

## 数据库迁移

### 新增字段
```sql
ALTER TABLE user 
ADD COLUMN membership_start_date TIMESTAMP NULL COMMENT '会员开始时间',
ADD COLUMN monthly_book_count INT DEFAULT 0 COMMENT '当前会员月内创建的卡册数量';

-- 添加索引
ALTER TABLE user ADD INDEX idx_membership_start_date (membership_start_date);
```

### 数据初始化
```sql
-- 为现有订阅用户设置默认的会员开始时间
UPDATE user 
SET membership_start_date = created_at 
WHERE membership_type IN ('subscription', 'both') 
AND membership_start_date IS NULL;
```

## 配置参数

### 可配置的限制参数
```go
const (
    MonthlyBookLimit = 30  // 每月最大卡册数量
    MembershipMonthDays = 30  // 会员月天数
)
```

## 监控和日志

### 关键日志
- 月度计数重置：`Monthly book count reset`
- 月度计数增加：`Failed to increment monthly book count`
- 权限检查：`Failed to reset monthly book count`

### 监控指标
- 月度卡册创建数量分布
- 达到限制的用户数量
- 月度重置频率

## 总结

月度卡册限制功能已完全实现，支持：

1. ✅ 订阅会员月度30本卡册限制
2. ✅ 自动月度重置机制
3. ✅ 智能权限检查
4. ✅ 完整的API支持
5. ✅ 详细的用户反馈
6. ✅ 向后兼容性

用户现在可以享受更合理的订阅会员权益管理，系统也能更好地控制资源使用。
