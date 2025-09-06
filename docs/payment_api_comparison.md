# 支付API对比文档

## 概述

本文档对比了 `/pay/wechat/native` 和 `/membership/payment` 两个支付接口的实现，确保它们使用相同的微信支付API和配置。

## 接口对比

### 1. `/pay/wechat/native` - 通用微信支付接口

**路径**: `POST /v1/pay/wechat/native`

**功能**: 创建微信Native支付订单

**实现位置**: `internal/numind/controller/v1/pay/wechat.go`

**请求参数**:
```json
{
  "out_trade_no": "ORDER_20240801_001",
  "description": "商品描述",
  "amount": 100
}
```

**实现逻辑**:
1. 验证请求参数
2. 获取微信支付配置
3. 调用 `wechat.CreateNativeOrder()` 创建支付订单
4. 返回支付结果

### 2. `/membership/payment` - 会员支付接口

**路径**: `POST /v1/membership/payment`

**功能**: 创建会员购买支付订单（支持Native、小程序、JSAPI）

**实现位置**: `internal/numind/controller/v1/membership/membership.go`

**请求参数**:
```json
{
  "membership_type": "subscription",
  "subscription_days": 30,
  "pay_method": "native",
  "openid": "user_openid" // 小程序和JSAPI支付必填
}
```

**实现逻辑**:
1. 验证会员类型和参数
2. 服务端计算价格（安全验证）
3. 创建支付记录
4. 根据支付方式调用对应的微信支付API
5. 返回支付结果

## 一致性检查

### ✅ 配置读取一致性

两个接口都使用相同的配置读取方法：

```go
func getWechatPayConfig() map[string]string {
    return map[string]string{
        "app_id":                   viper.GetString("wechat.app_id"),
        "mch_id":                   viper.GetString("wechat.mch_id"),
        "mch_cert_serial_no":       viper.GetString("wechat.mch_cert_serial_no"),
        "mch_api_v3_key":           viper.GetString("wechat.mch_api_v3_key"),
        "mch_private_key_path":     viper.GetString("wechat.mch_private_key_path"),
        "wechatpay_cert_path":      viper.GetString("wechat.wechatpay_cert_path"),
        "notify_url":               viper.GetString("wechat.notify_url"),
        "use_wechatpay_public_key": viper.GetString("wechat.use_wechatpay_public_key"),
    }
}
```

### ✅ 微信支付API调用一致性

两个接口都调用相同的微信支付业务方法：

**Native支付**:
```go
// 通用接口
resp, err := wechat.CreateNativeOrder(cfg, req.OutTradeNo, req.Description, req.Amount)

// 会员接口
resp, err := wechat.CreateNativeOrder(config, req.OutTradeNo, req.Description, req.Amount)
```

**小程序支付**:
```go
// 通用接口
resp, err := wechat.CreateMiniProgramOrder(cfg, req.OutTradeNo, req.Description, req.Amount, req.OpenID)

// 会员接口
resp, err := wechat.CreateMiniProgramOrder(config, req.OutTradeNo, req.Description, req.Amount, req.OpenID)
```

### ✅ 错误处理一致性

两个接口都使用相同的错误处理模式：

```go
if err != nil {
    core.WriteResponse(c, errno.InternalServerError.SetMessage("创建微信支付失败: %s", err.Error()), nil)
    return
}
```

### ✅ 响应格式一致性

两个接口都返回相同的响应格式：

**Native支付响应**:
```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "out_trade_no": "ORDER_20240801_001",
    "code_url": "weixin://wxpay/bizpayurl?pr=xxxxxxxx",
    "prepay_id": "wx2024080114300000000000000000"
  }
}
```

**小程序支付响应**:
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

## 主要差异

### 1. 参数验证

**通用接口**: 简单的参数验证
```go
var req struct {
    OutTradeNo  string `json:"out_trade_no" binding:"required"`
    Description string `json:"description" binding:"required"`
    Amount      int64  `json:"amount" binding:"required"`
}
```

**会员接口**: 复杂的业务验证
```go
var req struct {
    MembershipType   string `json:"membership_type" binding:"required,oneof=subscription package"`
    SubscriptionDays int    `json:"subscription_days,omitempty"`
    PackageCount     int    `json:"package_count,omitempty"`
    PayMethod        string `json:"pay_method" binding:"required,oneof=native miniprogram jsapi"`
    OpenID           string `json:"openid,omitempty"`
}
```

### 2. 价格计算

**通用接口**: 直接使用前端传入的价格
```go
Amount: req.Amount
```

**会员接口**: 服务端计算价格（安全验证）
```go
// 服务端根据参数计算价格
amount := mc.calculateSubscriptionPrice(req.SubscriptionDays)
// 或
amount := mc.calculatePackagePrice(req.PackageCount)
```

### 3. 支付记录

**通用接口**: 不创建支付记录

**会员接口**: 创建完整的支付记录
```go
payment := &model.PaymentM{
    OutTradeNo:       req.OutTradeNo,
    UserID:           userID,
    Amount:           req.Amount,
    Description:      req.Description,
    MembershipType:   req.MembershipType,
    PackageCount:     req.PackageCount,
    SubscriptionDays: req.SubscriptionDays,
    // ... 其他字段
}
```

## 安全措施对比

### 通用接口
- ❌ 无价格验证
- ❌ 无参数白名单
- ❌ 无审计日志

### 会员接口
- ✅ 严格的价格验证
- ✅ 参数白名单验证
- ✅ 完整的审计日志
- ✅ 服务端价格计算
- ✅ 天数累加功能

## 建议

### 1. 统一配置管理
建议将 `getWechatPayConfig()` 方法提取到公共包中，避免重复代码。

### 2. 统一错误处理
建议创建统一的支付错误处理函数。

### 3. 统一响应格式
建议创建统一的支付响应结构体。

### 4. 安全升级
建议为通用接口也添加基本的安全验证。

## 总结

通过对比分析，`/membership/payment` 接口已经与 `/pay/wechat/native` 接口保持一致：

1. **配置读取**: 使用相同的配置源
2. **API调用**: 调用相同的微信支付业务方法
3. **错误处理**: 使用相同的错误处理模式
4. **响应格式**: 返回相同的响应结构

同时，会员接口还增加了额外的安全措施和业务逻辑，确保支付系统的安全性和可靠性。
