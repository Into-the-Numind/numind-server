# 会员购买API文档

## 概述

本文档描述了支持年度、月度、包次数三种会员类型的微信购买API接口。

## 会员类型

- `monthly`: 月度会员 (30元/月)
- `yearly`: 年度会员 (300元/年)  
- `package`: 资源包会员 (1元/次)

## API接口

### 1. 创建会员购买支付

**接口地址**: `POST /v1/membership/payment`

**请求头**: 
```
Authorization: Bearer <token>
Content-Type: application/json
```

**请求参数**:
```json
{
  "membership_type": "monthly|yearly|package",
  "package_count": 10,  // 仅当membership_type为package时使用
  "pay_method": "native|miniprogram|jsapi",
  "openid": "user_openid"  // 小程序支付必填
}
```

**响应示例**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "out_trade_no": "membership_123_1234567890",
    "code_url": "weixin://wxpay/bizpayurl?pr=example",
    "message": "请使用微信扫描二维码完成支付"
  }
}
```

### 2. 获取用户会员信息

**接口地址**: `GET /v1/membership/info`

**请求头**: 
```
Authorization: Bearer <token>
```

**响应示例**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "membership_type": "monthly",
    "membership_expires": "2024-02-01T00:00:00Z",
    "package_count": 0,
    "is_pro": true,
    "membership_status": "月度会员",
    "is_active": true
  }
}
```

### 3. 获取会员套餐信息

**接口地址**: `GET /membership/plans`

**请求头**: 无需鉴权

**响应示例**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "plans": [
      {
        "type": "monthly",
        "name": "月度会员",
        "price": 3000,
        "description": "享受月度会员权益",
        "features": ["无限次生成", "高级模板", "优先客服"]
      },
      {
        "type": "yearly", 
        "name": "年度会员",
        "price": 30000,
        "description": "享受年度会员权益，更优惠",
        "features": ["无限次生成", "高级模板", "优先客服", "专属功能"]
      },
      {
        "type": "package",
        "name": "资源包",
        "price": 100,
        "description": "按次购买，灵活使用", 
        "features": ["按需购买", "永久有效", "灵活使用"]
      }
    ]
  }
}
```

## 支付流程

1. 用户选择会员类型和支付方式
2. 调用创建支付接口，获取支付信息
3. 用户完成支付
4. 微信支付回调更新支付状态
5. 系统自动更新用户会员状态

## 会员状态更新逻辑

### 月度/年度会员
- 设置会员类型和到期时间
- 如果用户已有未过期会员，延长到期时间
- 设置用户为付费用户

### 包次数会员
- 设置会员类型为package
- 增加包次数
- 设置用户为付费用户

## 错误码

- `400`: 参数错误
- `401`: 用户未登录
- `500`: 服务器内部错误

## 注意事项

1. 包次数会员的`package_count`必须大于0
2. 小程序支付必须提供`openid`
3. 支付成功后系统会自动更新用户会员状态
4. 会员到期时间基于当前时间计算
