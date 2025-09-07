# Both 会员类型使用示例

## 场景1：订阅会员购买资源包

### 用户初始状态
```json
{
  "membership_type": "subscription",
  "membership_expires": "2024-02-01T00:00:00Z",
  "package_count": 0,
  "is_pro": true
}
```

### 购买资源包请求
```bash
curl -X POST http://localhost:9091/v1/membership/payment \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "membership_type": "package",
    "package_count": 5,
    "pay_method": "native"
  }'
```

### 购买后状态
```json
{
  "membership_type": "both",
  "membership_expires": "2024-02-01T00:00:00Z",
  "package_count": 5,
  "is_pro": true
}
```

## 场景2：资源包用户订阅

### 用户初始状态
```json
{
  "membership_type": "package",
  "membership_expires": null,
  "package_count": 3,
  "is_pro": true
}
```

### 购买订阅会员请求
```bash
curl -X POST http://localhost:9091/v1/membership/payment \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "membership_type": "subscription",
    "subscription_days": 30,
    "pay_method": "native"
  }'
```

### 购买后状态
```json
{
  "membership_type": "both",
  "membership_expires": "2024-02-01T00:00:00Z",
  "package_count": 3,
  "is_pro": true
}
```

## 场景3：both用户继续购买

### 用户当前状态
```json
{
  "membership_type": "both",
  "membership_expires": "2024-02-01T00:00:00Z",
  "package_count": 2,
  "is_pro": true
}
```

### 购买更多资源包
```bash
curl -X POST http://localhost:9091/v1/membership/payment \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "membership_type": "package",
    "package_count": 20,
    "pay_method": "native"
  }'
```

### 购买后状态
```json
{
  "membership_type": "both",
  "membership_expires": "2024-02-01T00:00:00Z",
  "package_count": 22,  // 2 + 20
  "is_pro": true
}
```

### 购买更多订阅天数
```bash
curl -X POST http://localhost:9091/v1/membership/payment \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "membership_type": "subscription",
    "subscription_days": 365,
    "pay_method": "native"
  }'
```

### 购买后状态
```json
{
  "membership_type": "both",
  "membership_expires": "2025-02-01T00:00:00Z",  // 延长1年
  "package_count": 22,
  "is_pro": true
}
```

## 会员状态显示

### 都有效
```json
{
  "membership_status": "订阅会员+资源包（剩余22次）",
  "can_use_subscription": true,
  "can_use_package": true
}
```

### 只有订阅会员有效
```json
{
  "membership_status": "订阅会员",
  "can_use_subscription": true,
  "can_use_package": false
}
```

### 只有资源包有效
```json
{
  "membership_status": "资源包会员（剩余5次）",
  "can_use_subscription": false,
  "can_use_package": true
}
```

### 都无效
```json
{
  "membership_status": "免费用户",
  "can_use_subscription": false,
  "can_use_package": false
}
```

## 创建卡册权限

### both用户创建卡册
```bash
curl -X GET http://localhost:9091/v1/membership/create-permission \
  -H "Authorization: Bearer YOUR_TOKEN"
```

### 响应
```json
{
  "code": 0,
  "data": {
    "can_create": true,
    "reason": "订阅会员+资源包（剩余22次）有效，可以创建卡册",
    "membership_type": "both",
    "is_pro": true,
    "package_count": 22,
    "membership_expires": "2025-02-01T00:00:00Z"
  }
}
```

## 消费使用次数

### 消费请求
```bash
curl -X POST http://localhost:9091/v1/membership/consume-usage \
  -H "Authorization: Bearer YOUR_TOKEN"
```

### 消费逻辑
1. **优先使用订阅会员**：如果订阅会员有效，优先消费订阅会员权益
2. **其次使用资源包**：如果订阅会员无效但资源包有效，消费资源包次数
3. **更新状态**：消费后自动更新用户状态

### 消费后状态变化
- 订阅会员有效时：`package_count` 不变
- 只有资源包有效时：`package_count` 减1
- 如果资源包用完且订阅会员过期：`membership_type` 可能变为 `"free"`

## 注意事项

1. **类型转换是自动的**：系统会根据用户当前状态自动决定新的会员类型
2. **权益累加**：订阅天数会累加，资源包次数会累加
3. **权限优先**：创建卡册时优先使用订阅会员权益
4. **状态显示**：会员状态会根据实际有效权益动态显示
5. **向后兼容**：现有用户数据完全兼容，不会影响现有功能
