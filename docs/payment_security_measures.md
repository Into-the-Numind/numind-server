# 支付安全措施总结

## 概述

为了防止用户通过修改前端代码来篡改价格，我们实现了完整的安全验证机制，确保支付金额完全由服务端控制。

## 安全措施

### 1. 服务端价格验证

#### 订阅会员价格验证
- **30天订阅**：严格验证价格为2800分（28元）
- **365天订阅**：严格验证价格为19800分（198元）
- **价格白名单**：只允许这两个价格，其他价格一律拒绝

#### 资源包价格验证
- **1次包**：300分（3元）
- **5次包**：1200分（12元）
- **20次包**：3800分（38元）
- **50次包**：5000分（50元）
- **价格白名单**：只允许这四个标准配置

### 2. 服务端计算价格

#### 订阅会员
- 前端只传递订阅天数（30或365）
- 服务端根据天数计算对应价格
- 防止前端直接传递价格参数

#### 资源包
- 前端只传递包次数（1、5、20、50）
- 服务端根据次数计算对应价格
- 严格验证次数是否在允许范围内

### 3. 参数验证

#### 订阅会员验证
```go
// 验证订阅天数只能是30或365
if req.SubscriptionDays != 30 && req.SubscriptionDays != 365 {
    return fmt.Errorf("订阅天数只支持30天和365天")
}
```

#### 资源包验证
```go
// 验证包次数是否在允许的范围内
validCounts := []int{1, 5, 20, 50}
isValidCount := false
for _, count := range validCounts {
    if req.PackageCount == count {
        isValidCount = true
        break
    }
}
if !isValidCount {
    return fmt.Errorf("包次数只支持1、5、20、50次")
}
```

### 4. 价格验证器

创建了专门的价格验证器 `PriceValidator`：

```go
type PriceValidator struct {
    subscriptionPrices map[int64]int // 订阅价格映射：价格(分) -> 天数
    packagePrices      map[int]int64 // 资源包价格映射：次数 -> 价格(分)
}
```

#### 主要功能
- `ValidateSubscriptionPrice()`: 验证订阅价格
- `ValidatePackagePrice()`: 验证资源包价格
- `GetSubscriptionPrice()`: 服务端计算订阅价格
- `GetPackagePrice()`: 服务端计算资源包价格

### 5. 审计日志记录

#### 支付审计日志模型
```go
type PaymentAuditLog struct {
    OutTradeNo       string    // 商户订单号
    UserID           uint      // 用户ID
    Amount           int64     // 支付金额(分)
    MembershipType   string    // 会员类型
    PackageCount     int       // 包次数
    SubscriptionDays int       // 订阅天数
    Status           string    // 支付状态
    TransactionID    string    // 微信支付订单号
    PaidAt           *time.Time // 支付时间
}
```

#### 审计记录内容
- 完整的支付金额用于审计
- 用户ID和订单号用于追踪
- 会员类型和具体参数
- 支付状态变更记录
- 时间戳用于审计追踪

### 6. 用户状态更新

#### 购买资源包后自动更新is_pro字段
```go
case model.MembershipTypePackage:
    // 包次数类型，增加次数
    if err := b.ds.DB().Model(&model.User{}).Where("id = ?", payment.UserID).
        UpdateColumns(map[string]interface{}{
            "membership_type": model.MembershipTypePackage,
            "is_pro":          true,  // 购买资源包后设置为pro用户
            "package_count":   gorm.Expr("package_count + ?", payment.PackageCount),
        }).Error; err != nil {
        return fmt.Errorf("更新用户包次数失败: %w", err)
    }
```

## 安全防护效果

### 1. 防止价格篡改
- 用户无法通过修改前端代码来改变价格
- 所有价格计算都在服务端进行
- 严格的价格白名单验证

### 2. 防止参数伪造
- 订阅天数只能是30或365
- 资源包次数只能是1、5、20、50
- 所有参数都经过严格验证

### 3. 完整的审计追踪
- 记录所有支付金额和参数
- 便于后续审计和问题排查
- 防止恶意操作

### 4. 自动状态管理
- 购买资源包后自动设置is_pro为true
- 订阅会员自动设置is_pro为true
- 确保用户权益正确生效

## 使用示例

### 前端调用示例

#### 订阅会员
```json
{
    "membership_type": "subscription",
    "subscription_days": 30,  // 或 365
    "pay_method": "miniprogram",
    "openid": "user_openid"
}
```

#### 资源包
```json
{
    "membership_type": "package",
    "package_count": 5,  // 1, 5, 20, 50
    "pay_method": "miniprogram",
    "openid": "user_openid"
}
```

### 服务端处理流程

1. **参数验证**：验证会员类型和具体参数
2. **价格计算**：服务端根据参数计算价格
3. **价格验证**：使用价格验证器验证计算出的价格
4. **创建支付**：创建支付记录
5. **审计日志**：记录支付审计日志
6. **状态更新**：支付成功后更新用户状态

## 总结

通过以上安全措施，我们确保了：

1. **服务端价格验证**：严格验证订阅天数和价格的对应关系
2. **价格白名单**：只允许预定义的价格配置
3. **服务端计算**：价格完全由服务端计算，前端无法篡改
4. **参数验证**：严格验证订阅天数和资源包次数
5. **数据库记录**：保存完整的支付金额用于审计
6. **自动状态管理**：购买资源包后用户is_pro字段自动变为true

这些措施有效防止了用户通过修改前端代码来篡改价格，确保了支付系统的安全性和可靠性。
