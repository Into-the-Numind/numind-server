# 支付API使用示例

## 概述

本文档展示了如何正确使用新的安全支付API，包括订阅会员和资源包购买。

## API端点

### 1. 获取会员套餐信息

**GET** `/v1/membership/plans`

无需鉴权，返回所有可用的会员套餐。

#### 响应示例

```json
{
    "code": 0,
    "message": "success",
    "data": {
        "plans": [
            {
                "type": "subscription",
                "name": "月度订阅会员",
                "price": 2800,
                "days": 30,
                "description": "享受月度订阅会员权益",
                "features": ["30次/月卡册创建", "无水印", "解锁全部模板", "高峰期优先处理"]
            },
            {
                "type": "subscription",
                "name": "年度订阅会员",
                "price": 19800,
                "days": 365,
                "description": "享受年度订阅会员权益，约16.5元/月，立省40%",
                "features": ["30次/月卡册创建", "无水印", "解锁全部模板", "高峰期优先处理", "年度优惠价格"]
            },
            {
                "type": "package",
                "name": "1次创作包",
                "price": 300,
                "count": 1,
                "unit_price": 300,
                "description": "单次使用，适合偶尔使用",
                "features": ["按次计费", "灵活使用", "适合偶尔使用"]
            },
            {
                "type": "package",
                "name": "5次创作包",
                "price": 1200,
                "count": 5,
                "unit_price": 240,
                "description": "5次使用，单次成本2.4元",
                "features": ["按次计费", "灵活使用", "单次成本优惠"]
            },
            {
                "type": "package",
                "name": "20次创作包",
                "price": 3800,
                "count": 20,
                "unit_price": 190,
                "description": "20次使用，单次成本1.9元",
                "features": ["按次计费", "灵活使用", "单次成本更优惠"]
            },
            {
                "type": "package",
                "name": "50次创作包",
                "price": 5000,
                "count": 50,
                "unit_price": 100,
                "description": "50次使用，单次成本1.0元",
                "features": ["按次计费", "灵活使用", "单次成本最优惠"]
            }
        ]
    }
}
```

### 2. 创建会员购买支付

**POST** `/v1/membership/payment`

需要鉴权，创建会员购买支付订单。

#### 请求头

```
Authorization: Bearer <jwt_token>
Content-Type: application/json
```

#### 订阅会员购买

```json
{
    "membership_type": "subscription",
    "subscription_days": 30,
    "pay_method": "miniprogram",
    "openid": "user_openid_here"
}
```

或年度订阅：

```json
{
    "membership_type": "subscription",
    "subscription_days": 365,
    "pay_method": "miniprogram",
    "openid": "user_openid_here"
}
```

#### 资源包购买

```json
{
    "membership_type": "package",
    "package_count": 5,
    "pay_method": "miniprogram",
    "openid": "user_openid_here"
}
```

#### 响应示例

```json
{
    "code": 0,
    "message": "success",
    "data": {
        "out_trade_no": "membership_123_1234567890",
        "prepay_id": "wx123456789",
        "pay_sign": "example_sign",
        "time_stamp": "1234567890",
        "nonce_str": "example_nonce",
        "package": "prepay_id=wx123456789",
        "sign_type": "RSA",
        "message": "请调用微信支付完成支付"
    }
}
```

### 3. 获取用户会员信息

**GET** `/v1/membership/info`

需要鉴权，获取当前用户的会员信息。

#### 响应示例

```json
{
    "code": 0,
    "message": "success",
    "data": {
        "membership_type": "subscription",
        "membership_expires": "2024-02-01T00:00:00Z",
        "package_count": 0,
        "package_info": {
            "remaining_count": 0,
            "description": "非资源包会员",
            "can_use": false
        },
        "is_pro": true,
        "membership_status": "订阅会员",
        "is_active": true
    }
}
```

### 4. 检查创建权限

**GET** `/v1/membership/permission`

需要鉴权，检查用户是否有创建卡册的权限。

#### 响应示例

```json
{
    "code": 0,
    "message": "success",
    "data": {
        "can_create": true,
        "reason": "订阅会员有效，可以创建卡册",
        "membership_type": "subscription",
        "is_pro": true,
        "package_count": 0,
        "book_all_num": 5,
        "membership_expires": "2024-02-01T00:00:00Z"
    }
}
```

### 5. 消费使用次数

**POST** `/v1/membership/consume`

需要鉴权，消费用户的使用次数（创建卡册时调用）。

#### 响应示例

```json
{
    "code": 0,
    "message": "success",
    "data": {
        "message": "使用次数消费成功",
        "usage_type": "subscription",
        "remaining": 0,
        "membership_type": "subscription"
    }
}
```

## 前端集成示例

### JavaScript/TypeScript 示例

```typescript
// 获取会员套餐信息
async function getMembershipPlans() {
    const response = await fetch('/v1/membership/plans');
    const data = await response.json();
    return data.data.plans;
}

// 购买订阅会员
async function purchaseSubscription(days: 30 | 365, openid: string) {
    const response = await fetch('/v1/membership/payment', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${getToken()}`
        },
        body: JSON.stringify({
            membership_type: 'subscription',
            subscription_days: days,
            pay_method: 'miniprogram',
            openid: openid
        })
    });
    return await response.json();
}

// 购买资源包
async function purchasePackage(count: 1 | 5 | 20 | 50, openid: string) {
    const response = await fetch('/v1/membership/payment', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Authorization': `Bearer ${getToken()}`
        },
        body: JSON.stringify({
            membership_type: 'package',
            package_count: count,
            pay_method: 'miniprogram',
            openid: openid
        })
    });
    return await response.json();
}

// 检查创建权限
async function checkCreatePermission() {
    const response = await fetch('/v1/membership/permission', {
        headers: {
            'Authorization': `Bearer ${getToken()}`
        }
    });
    return await response.json();
}

// 消费使用次数
async function consumeUsage() {
    const response = await fetch('/v1/membership/consume', {
        method: 'POST',
        headers: {
            'Authorization': `Bearer ${getToken()}`
        }
    });
    return await response.json();
}
```

### React 组件示例

```jsx
import React, { useState, useEffect } from 'react';

function MembershipPlans() {
    const [plans, setPlans] = useState([]);
    const [loading, setLoading] = useState(true);

    useEffect(() => {
        fetchPlans();
    }, []);

    const fetchPlans = async () => {
        try {
            const response = await fetch('/v1/membership/plans');
            const data = await response.json();
            setPlans(data.data.plans);
        } catch (error) {
            console.error('获取套餐信息失败:', error);
        } finally {
            setLoading(false);
        }
    };

    const handlePurchase = async (plan) => {
        try {
            let requestBody;
            
            if (plan.type === 'subscription') {
                requestBody = {
                    membership_type: 'subscription',
                    subscription_days: plan.days,
                    pay_method: 'miniprogram',
                    openid: getOpenId()
                };
            } else if (plan.type === 'package') {
                requestBody = {
                    membership_type: 'package',
                    package_count: plan.count,
                    pay_method: 'miniprogram',
                    openid: getOpenId()
                };
            }

            const response = await fetch('/v1/membership/payment', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': `Bearer ${getToken()}`
                },
                body: JSON.stringify(requestBody)
            });

            const result = await response.json();
            if (result.code === 0) {
                // 调用微信支付
                callWechatPay(result.data);
            } else {
                alert('创建支付失败: ' + result.message);
            }
        } catch (error) {
            console.error('购买失败:', error);
            alert('购买失败，请重试');
        }
    };

    if (loading) return <div>加载中...</div>;

    return (
        <div className="membership-plans">
            {plans.map((plan, index) => (
                <div key={index} className="plan-card">
                    <h3>{plan.name}</h3>
                    <p className="price">¥{(plan.price / 100).toFixed(2)}</p>
                    <p className="description">{plan.description}</p>
                    <ul className="features">
                        {plan.features.map((feature, idx) => (
                            <li key={idx}>{feature}</li>
                        ))}
                    </ul>
                    <button onClick={() => handlePurchase(plan)}>
                        立即购买
                    </button>
                </div>
            ))}
        </div>
    );
}

export default MembershipPlans;
```

## 安全注意事项

### 1. 前端安全
- **不要在前端存储价格信息**：价格完全由服务端计算
- **不要在前端验证价格**：所有验证都在服务端进行
- **使用HTTPS**：确保所有API调用都通过HTTPS进行

### 2. 参数验证
- **订阅天数**：只能是30或365
- **资源包次数**：只能是1、5、20、50
- **支付方式**：只能是native、miniprogram、jsapi

### 3. 错误处理
- 所有API调用都应该包含适当的错误处理
- 显示用户友好的错误信息
- 记录错误日志用于调试

### 4. 用户体验
- 在支付过程中显示加载状态
- 提供清晰的错误提示
- 支付成功后更新用户界面

## 测试用例

### 1. 正常流程测试
- 获取套餐信息
- 购买月度订阅
- 购买年度订阅
- 购买各种资源包
- 检查权限
- 消费次数

### 2. 安全测试
- 尝试传递无效的订阅天数
- 尝试传递无效的资源包次数
- 尝试传递恶意价格参数
- 测试未授权访问

### 3. 边界测试
- 测试最大/最小参数值
- 测试空参数
- 测试无效的支付方式
