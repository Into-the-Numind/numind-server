# 微信支付 API 文档

## 概述

微信支付 API 提供 Native 支付和小程序支付功能。

## 基础信息

- **Base URL**: `/v1`
- **认证**: 需要 Bearer Token
- **Content-Type**: `application/json`

## API 端点

### 1. 微信 Native 支付下单

**POST** `/v1/pay/wechat/native`

创建微信 Native 支付订单，返回支付二维码链接。

#### 请求参数

```json
{
  "out_trade_no": "ORDER_20240801_001",
  "description": "商品描述",
  "amount": 100
}
```

#### 响应示例

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "prepay_id": "wx2024080114300000000000000000",
    "code_url": "weixin://wxpay/bizpayurl?pr=xxxxxxxx"
  }
}
```

### 2. 微信小程序支付下单

**POST** `/v1/pay/wechat/miniprogram`

创建微信小程序支付订单，返回完整的支付参数。

#### 请求参数

```json
{
  "out_trade_no": "ORDER_20240801_001",
  "description": "商品描述",
  "amount": 100,
  "openid": "oUpF8uMuAJO_M2pxb1Q9zNjWeS6o"
}
```

#### 参数说明

| 参数名 | 类型 | 必填 | 说明 |
|--------|------|------|------|
| `out_trade_no` | string | 是 | 商户订单号，需确保唯一性 |
| `description` | string | 是 | 商品描述 |
| `amount` | int64 | 是 | 订单金额（分） |
| `openid` | string | 是 | 用户的 OpenID |

#### 响应示例

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "timeStamp": "1234567890",
    "nonceStr": "random_string",
    "package": "prepay_id=wx2024080114300000000000000000",
    "signType": "RSA",
    "paySign": "signature_string"
  }
}
```

#### 响应字段说明

| 字段名 | 类型 | 说明 |
|--------|------|------|
| `timeStamp` | string | 时间戳，从1970年1月1日00:00:00至今的秒数 |
| `nonceStr` | string | 随机字符串，不长于32位 |
| `package` | string | 统一下单接口返回的 prepay_id 参数值 |
| `signType` | string | 签名类型，默认为 RSA |
| `paySign` | string | 支付签名 |

## 错误码

| 错误码 | 说明 |
|--------|------|
| 10001 | 参数绑定错误 |
| 10002 | 参数验证错误 |
| 10005 | 微信支付配置错误 |
| 10006 | 微信支付 API 调用失败 |

## 使用示例

### Native 支付（二维码支付）

```javascript
// 创建 Native 支付订单
const response = await fetch('/v1/pay/wechat/native', {
  method: 'POST',
  headers: {
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ' + token
  },
  body: JSON.stringify({
    out_trade_no: 'ORDER_' + Date.now(),
    description: '商品购买',
    amount: 10000  // 100元
  })
});
```

### 小程序支付

```javascript
// 创建小程序支付订单
const response = await fetch('/v1/pay/wechat/miniprogram', {
  method: 'POST',
  headers: {
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ' + token
  },
  body: JSON.stringify({
    out_trade_no: 'ORDER_' + Date.now(),
    description: '商品购买',
    amount: 10000,  // 100元
    openid: 'user_openid_here'
  })
});

const result = await response.json();
if (result.code === 0) {
  // 调用小程序支付
  wx.requestPayment({
    timeStamp: result.data.timeStamp,
    nonceStr: result.data.nonceStr,
    package: result.data.package,
    signType: result.data.signType,
    paySign: result.data.paySign,
    success: function(res) {
      console.log('支付成功', res);
    },
    fail: function(res) {
      console.log('支付失败', res);
    }
  });
}
```

## 注意事项

1. 订单号必须唯一
2. 金额单位为分
3. 需要配置微信支付证书
4. 小程序支付需要用户的 OpenID
5. 回调地址需要 HTTPS 