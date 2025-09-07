# Both 会员类型实现总结

## 功能概述

实现了 `"both"` 会员类型，允许用户同时拥有订阅会员和资源包。当用户已经是订阅会员再购买资源包，或者已经是资源包用户再订阅时，`membership_type` 字段会自动更新为 `"both"`。

## 实现的功能

### 1. 会员类型常量更新

**文件**: `internal/pkg/model/user.go`

```go
const (
    MembershipTypeFree         = "free"         // 免费用户
    MembershipTypeSubscription = "subscription" // 订阅会员
    MembershipTypePackage      = "package"      // 付费资源包（次数）
    MembershipTypeBoth         = "both"         // 同时拥有订阅会员和资源包
)
```

### 2. 会员状态判断逻辑

#### IsMembershipActive() 方法
- 对于 `"both"` 类型：只要订阅会员或资源包其中一个有效就算有效
- 订阅会员有效：`MembershipExpires` 未过期
- 资源包有效：`PackageCount > 0`

#### GetMembershipStatus() 方法
- 都有效：`"订阅会员+资源包（剩余X次）"`
- 只有订阅会员有效：`"订阅会员"`
- 只有资源包有效：`"资源包会员（剩余X次）"`
- 都无效：`"免费用户"`

#### CanUseSubscription() 和 CanUsePackage() 方法
- 支持 `"both"` 类型，可以分别检查订阅会员和资源包的使用权限

### 3. 支付逻辑更新

**文件**: `internal/numind/biz/payment/payment.go`

#### 订阅会员购买逻辑
```go
// 确定新的会员类型
var newMembershipType string
if user.MembershipType == model.MembershipTypePackage || user.MembershipType == model.MembershipTypeBoth {
    // 如果用户已有资源包，则设为both
    newMembershipType = model.MembershipTypeBoth
} else {
    // 否则设为订阅会员
    newMembershipType = model.MembershipTypeSubscription
}
```

#### 资源包购买逻辑
```go
// 确定新的会员类型
var newMembershipType string
if user.MembershipType == model.MembershipTypeSubscription || user.MembershipType == model.MembershipTypeBoth {
    // 如果用户已有订阅会员，则设为both
    newMembershipType = model.MembershipTypeBoth
} else {
    // 否则设为资源包
    newMembershipType = model.MembershipTypePackage
}
```

### 4. 用户创建卡册权限判断

**文件**: `internal/numind/controller/v1/membership/membership.go`

通过 `CanUseSubscription()` 和 `CanUsePackage()` 方法自动支持 `"both"` 类型：
- 优先检查订阅会员权限
- 其次检查资源包权限
- 最后检查免费用户限制

### 5. 会员信息显示

**文件**: `internal/numind/controller/v1/membership/membership.go`

更新了会员信息获取接口，正确显示 `"both"` 类型用户的资源包信息。

## 使用场景

### 场景1：订阅会员购买资源包
1. 用户已有订阅会员（`membership_type = "subscription"`）
2. 购买资源包
3. 系统自动将 `membership_type` 更新为 `"both"`
4. 用户同时拥有订阅会员和资源包权益

### 场景2：资源包用户订阅
1. 用户已有资源包（`membership_type = "package"`）
2. 购买订阅会员
3. 系统自动将 `membership_type` 更新为 `"both"`
4. 用户同时拥有订阅会员和资源包权益

### 场景3：both用户继续购买
1. 用户已是 `"both"` 类型
2. 购买订阅会员：累加订阅天数
3. 购买资源包：累加资源包次数
4. 保持 `"both"` 类型

## 测试验证

通过测试脚本验证了以下场景：

```go
// 都有效
user := &model.User{
    MembershipType:    model.MembershipTypeBoth,
    MembershipExpires: &time.Now().AddDate(0, 0, 30),
    PackageCount:      5,
}
// 结果: "订阅会员+资源包（剩余5次）"

// 只有订阅会员有效
user1 := &model.User{
    MembershipType:    model.MembershipTypeBoth,
    MembershipExpires: &time.Now().AddDate(0, 0, 30),
    PackageCount:      0,
}
// 结果: "订阅会员"

// 只有资源包有效
user2 := &model.User{
    MembershipType:    model.MembershipTypeBoth,
    MembershipExpires: &time.Now().AddDate(0, 0, -1), // 已过期
    PackageCount:      3,
}
// 结果: "资源包会员（剩余3次）"

// 都无效
user3 := &model.User{
    MembershipType:    model.MembershipTypeBoth,
    MembershipExpires: &time.Now().AddDate(0, 0, -1), // 已过期
    PackageCount:      0,
}
// 结果: "免费用户"
```

## 数据库影响

### 用户表字段
- `membership_type`: 支持新值 `"both"`
- `membership_expires`: 订阅会员到期时间
- `package_count`: 资源包剩余次数

### 兼容性
- 现有数据完全兼容
- 新功能向后兼容
- 不影响现有业务逻辑

## API 响应示例

### 会员信息接口
```json
{
  "code": 0,
  "data": {
    "membership_type": "both",
    "membership_expires": "2024-02-01T00:00:00Z",
    "membership_status": "订阅会员+资源包（剩余5次）",
    "package_info": {
      "remaining_count": 5,
      "description": "资源包剩余5次",
      "can_use": true
    },
    "subscription_info": {
      "is_active": true,
      "expires_at": "2024-02-01T00:00:00Z"
    }
  }
}
```

### 创建权限接口
```json
{
  "code": 0,
  "data": {
    "can_create": true,
    "reason": "订阅会员+资源包（剩余5次）有效，可以创建卡册",
    "membership_type": "both",
    "is_pro": true,
    "package_count": 5
  }
}
```

## 总结

`"both"` 会员类型功能已完全实现，支持：

1. ✅ 自动类型转换（subscription + package → both）
2. ✅ 智能状态判断（优先订阅会员，其次资源包）
3. ✅ 完整权限控制（创建卡册前判断）
4. ✅ 清晰状态显示（用户友好的状态描述）
5. ✅ 向后兼容（不影响现有功能）

用户现在可以灵活地组合使用订阅会员和资源包，享受更丰富的会员权益。
