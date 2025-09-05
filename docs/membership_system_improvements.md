# 会员系统优化改进

## 概述

根据你的需求，我们对会员系统进行了以下主要改进：

1. **统一会员类型**：将月度和年度会员统一为"订阅会员"
2. **实现会员续期逻辑**：支持累积天数，避免重复购买时覆盖原有会员期
3. **实现使用优先级**：会员次数优先于资源包次数使用

## 主要变更

### 1. 数据模型优化

#### User 模型更新
- 将 `MembershipType` 从 `free, monthly, yearly, package` 简化为 `free, subscription, package`
- 新增 `SubscriptionType` 字段用于区分月度/年度订阅
- 新增辅助方法：
  - `CanUseSubscription()`: 检查是否可以使用订阅会员权益
  - `CanUsePackage()`: 检查是否可以使用资源包
  - `GetAvailableUsageCount()`: 获取可用次数（优先返回订阅会员）

#### Payment 模型更新
- 更新 `MembershipType` 字段支持新的类型
- 新增 `SubscriptionType` 字段记录订阅类型

### 2. 会员续期逻辑

#### 核心特性
- **累积天数**：如果用户已有订阅会员且未过期，新购买的会员期会在现有到期时间基础上延长
- **类型统一**：月度和年度会员都归类为订阅会员，通过 `SubscriptionType` 区分
- **自动续期**：支持从月度会员升级到年度会员，或同类型续期

#### 实现逻辑
```go
// 如果用户已有订阅会员且未过期，延长到期时间
if user.MembershipType == model.MembershipTypeSubscription &&
    user.MembershipExpires != nil &&
    user.MembershipExpires.After(now) {
    // 在现有到期时间基础上延长
    if payment.SubscriptionType == model.SubscriptionTypeMonthly {
        newExpires := user.MembershipExpires.AddDate(0, 1, 0)
        updateData["membership_expires"] = &newExpires
    } else if payment.SubscriptionType == model.SubscriptionTypeYearly {
        newExpires := user.MembershipExpires.AddDate(1, 0, 0)
        updateData["membership_expires"] = &newExpires
    }
}
```

### 3. 使用优先级系统

#### 优先级规则
1. **订阅会员优先**：如果用户有有效的订阅会员，优先使用订阅权益（无限制）
2. **资源包次之**：订阅会员无效时，使用资源包次数
3. **免费用户限制**：最后检查免费用户限制

#### 实现方法
```go
func (u *User) GetAvailableUsageCount() (int, string) {
    // 优先使用订阅会员
    if u.CanUseSubscription() {
        return -1, "subscription" // -1 表示无限制
    }
    
    // 其次使用资源包
    if u.CanUsePackage() {
        return u.PackageCount, "package"
    }
    
    return 0, "none"
}
```

### 4. API 接口更新

#### 新增接口
- `POST /api/v1/membership/consume`: 消费使用次数（实现优先级逻辑）

#### 更新接口
- `POST /api/v1/membership/payment`: 支持新的会员类型和订阅类型
- `GET /api/v1/membership/plans`: 更新套餐信息结构
- `GET /api/v1/membership/permission`: 使用新的优先级逻辑

#### 请求参数更新
```json
{
  "membership_type": "subscription",  // 或 "package"
  "subscription_type": "monthly",     // 或 "yearly"（仅当 membership_type 为 subscription 时）
  "package_count": 5,                 // 仅当 membership_type 为 package 时
  "pay_method": "miniprogram",
  "openid": "user_openid"
}
```

## 使用示例

### 场景1：月度会员续期
用户当前是月度会员，本月底到期，今天购买年度会员：
- 结果：会员类型变为订阅会员，订阅类型为年度，到期时间从本月底延长一年

### 场景2：混合使用
用户既有订阅会员又有资源包：
- 创建卡册时优先使用订阅会员权益（无限制）
- 订阅会员过期后，自动使用资源包次数

### 场景3：统计优化
通过支付记录可以轻松区分：
- 月度订阅：`membership_type=subscription, subscription_type=monthly`
- 年度订阅：`membership_type=subscription, subscription_type=yearly`
- 资源包：`membership_type=package`

## 数据库迁移

需要为现有用户数据添加 `subscription_type` 字段：

```sql
ALTER TABLE user ADD COLUMN subscription_type VARCHAR(20) DEFAULT '';
```

对于现有数据，建议：
1. 将 `membership_type` 为 `monthly` 的记录更新为 `subscription`，`subscription_type` 为 `monthly`
2. 将 `membership_type` 为 `yearly` 的记录更新为 `subscription`，`subscription_type` 为 `yearly`

## 优势

1. **逻辑简化**：统一会员类型，减少复杂度
2. **用户体验**：支持会员续期，避免浪费
3. **灵活使用**：优先级系统确保用户权益最大化
4. **统计友好**：通过支付记录可以轻松进行各种统计分析
5. **扩展性好**：未来可以轻松添加新的订阅类型

## 注意事项

1. 确保所有相关业务逻辑都使用新的优先级检查方法
2. 在创建卡册等需要消费次数的操作中，调用 `ConsumeUsage` 接口
3. 定期检查会员到期状态，及时更新用户权限
4. 考虑添加会员到期提醒功能

这个优化方案完全满足了你提出的需求，既简化了系统复杂度，又提供了更好的用户体验。
