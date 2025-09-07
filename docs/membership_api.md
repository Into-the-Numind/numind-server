# 会员购买API文档

## 概述

本文档描述了支持年度、月度、包次数三种会员类型的微信购买API接口。

## 会员类型

- `monthly`: 月度会员 (28元/月)
- `yearly`: 年度会员 (198元/年，约16.5元/月，立省40%)  
- `package`: 资源包会员 (按次付费)

### 资源包定价表
- 1次创作包: 3元 (3.0元/次)
- 5次创作包: 12元 (2.4元/次)
- 20次创作包: 38元 (1.9元/次)
- 50次创作包: 50元 (1.0元/次)

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
    "membership_type": "package",
    "membership_expires": null,
    "package_count": 15,
    "package_info": {
      "remaining_count": 15,
      "description": "资源包剩余15次",
      "can_use": true
    },
    "is_pro": true,
    "membership_status": "资源包会员（剩余15次）",
    "is_active": true
  }
}
```

**字段说明**:
- `membership_type`: 会员类型 (free/monthly/yearly/package)
- `membership_expires`: 会员到期时间 (月度/年度会员有效，其他为null)
- `package_count`: 资源包剩余次数
- `package_info`: 资源包详细信息
  - `remaining_count`: 剩余次数
  - `description`: 描述信息
  - `can_use`: 是否可以使用
- `is_pro`: 是否为付费用户
- `membership_status`: 会员状态描述
- `is_active`: 会员是否有效

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
        "price": 2800,
        "description": "享受月度会员权益",
        "features": ["30次/月卡册创建", "无水印", "解锁全部模板", "高峰期优先处理"]
      },
      {
        "type": "yearly", 
        "name": "年度会员",
        "price": 19800,
        "description": "享受年度会员权益，约16.5元/月，立省40%",
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

### 4. 检查用户创建卡册权限

**接口地址**: `GET /v1/membership/permission`

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
    "can_create": true,
    "reason": "会员有效，可以创建卡册",
    "membership_type": "monthly",
    "is_pro": true,
    "package_count": 0,
    "book_all_num": 2,
    "membership_expires": "2024-02-01T00:00:00Z"
  }
}
```

**权限判断逻辑**:
1. **会员有效**: 月度/年度会员未过期，可以创建
2. **资源包剩余**: 包次数会员有剩余次数，可以创建
3. **免费用户限制**: 免费用户最多创建3个卡册
4. **会员过期**: 会员过期且无剩余次数，不能创建

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
