# 支付回调流程测试指南

## 完整支付流程

### 流程图

```
1. 用户发起支付
   POST /v1/membership/payment
   ↓
   创建支付记录 (payments表, status=pending)
   ↓
   调用微信支付API创建订单
   ↓
   返回支付参数给前端

2. 用户完成支付（微信小程序）
   ↓
   微信支付平台处理支付
   ↓
   支付成功后，微信发送回调通知

3. 服务器接收回调
   POST /api/pay/wechat/notify
   ↓
   WechatPayNotify() 函数处理
   ↓
   解析回调数据（验证签名）
   ↓
   检查交易状态 (TradeState == "SUCCESS")
   ↓
   提取订单信息 (out_trade_no, transaction_id, paid_at)
   ↓
   调用 UpdatePaymentStatus()
   ↓
   更新 payments 表 (status: pending → success)
   ↓
   调用 handleMembershipPurchase()
   ↓
   更新 user 表:
     - membership_type = "subscription"
     - is_pro = true
     - membership_expires = 当前时间 + 订阅天数
     - 如果是新订阅: 重置 membership_start_date 和 monthly_book_count
   ↓
   返回成功响应给微信

4. 用户创建卡册
   POST /v1/books
   ↓
   检查会员权限:
     - 如果 CanUseSubscription() == true: 无限制创建
     - 如果会员过期: 返回错误 "会员已过期，请续费后再创建卡册"
     - 如果是免费用户: 检查月度限制（5个/月）
   ↓
   创建卡册
```

## 本地测试步骤

### 前置准备

1. **启动服务器**
```bash
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server
./numind -c config_local.yaml
```

2. **获取JWT Token**
- 通过微信登录接口获取: `POST /v1/wechat/login`
- 或使用已有的token

3. **准备测试工具**
- `test_payment_callback_local.sh`: 自动化测试脚本
- `test_manual_payment_success.go`: 手动触发支付成功处理

### 测试步骤

#### 步骤1: 检查用户当前状态

```bash
# 使用测试脚本
./test_payment_callback_local.sh YOUR_JWT_TOKEN

# 或手动执行
curl -X GET "http://localhost:9091/v1/membership/info" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" | jq '.'
```

**预期结果**:
- 查看当前会员类型、是否付费、到期时间等信息

#### 步骤2: 创建支付订单

```bash
curl -X POST "http://localhost:9091/v1/membership/payment" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "membership_type": "subscription",
    "subscription_days": 30,
    "pay_method": "miniprogram",
    "openid": "test_openid_123"
  }' | jq '.'
```

**预期结果**:
- 返回 `out_trade_no`（订单号）
- 数据库中 `payments` 表新增一条记录，`status = 'pending'`

**验证**:
```sql
SELECT * FROM payments WHERE out_trade_no = 'mem_xxx_xxx';
```

#### 步骤3: 模拟支付成功

由于本地测试无法接收真实的微信支付回调，我们需要手动触发支付成功处理。

**方法1: 使用测试工具（推荐）**

```bash
# 编译测试工具
go build -o test_manual_payment_success ./test_manual_payment_success.go

# 运行测试工具（替换为实际的订单号）
./test_manual_payment_success mem_xxx_xxx
```

**方法2: 直接修改数据库 + 手动触发**

```sql
-- 1. 更新支付状态
UPDATE payments 
SET status='success', 
    transaction_id='test_txn_123', 
    paid_at=NOW() 
WHERE out_trade_no='mem_xxx_xxx';

-- 2. 然后需要手动触发会员购买处理
-- 可以通过调用 UpdatePaymentStatus 来实现
```

#### 步骤4: 验证支付状态更新

```sql
SELECT * FROM payments WHERE out_trade_no = 'mem_xxx_xxx';
-- status 应该是 'success'
-- transaction_id 应该有值
-- paid_at 应该有值
```

#### 步骤5: 验证会员状态更新

```bash
curl -X GET "http://localhost:9091/v1/membership/info" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" | jq '.'
```

**预期结果**:
- `membership_type`: `"subscription"`
- `is_pro`: `true`
- `membership_expires`: 30天后的日期
- `membership_start_date`: 当前日期

**数据库验证**:
```sql
SELECT id, membership_type, is_pro, membership_expires, membership_start_date 
FROM user 
WHERE id = (SELECT user_id FROM payments WHERE out_trade_no = 'mem_xxx_xxx');
```

#### 步骤6: 测试创建卡册（订阅会员无限制）

```bash
# 第一次创建
curl -X POST "http://localhost:9091/v1/books" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -F "text=测试卡册1" | jq '.'

# 第二次创建（应该也成功，因为订阅会员无限制）
curl -X POST "http://localhost:9091/v1/books" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -F "text=测试卡册2" | jq '.'

# 第三次创建（应该也成功）
curl -X POST "http://localhost:9091/v1/books" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -F "text=测试卡册3" | jq '.'
```

**预期结果**:
- 所有创建请求都应该成功
- 返回卡册ID和基本信息

#### 步骤7: 测试过期会员（可选）

```sql
-- 手动修改会员到期时间为过去
UPDATE user 
SET membership_expires = DATE_SUB(NOW(), INTERVAL 1 DAY)
WHERE id = YOUR_USER_ID;
```

然后尝试创建卡册：

```bash
curl -X POST "http://localhost:9091/v1/books" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -F "text=测试卡册" | jq '.'
```

**预期结果**:
- 返回错误: `"会员已过期，请续费后再创建卡册"`
- HTTP状态码: 403

#### 步骤8: 查看日志

```bash
# 实时查看日志
tail -f numind.log | grep -E "pay|membership|book.*create"

# 查看特定订单的处理日志
grep "mem_xxx_xxx" numind.log

# 查看所有支付回调相关日志
grep -i "WechatPayNotify\|UpdatePaymentStatus\|handleMembershipPurchase" numind.log

# 查看创建卡册的日志
grep -i "Create book\|membership.*expired\|can.*create" numind.log
```

## 关键日志检查点

### 支付回调日志
- ✅ `WechatPayNotify callback received`: 回调请求开始
- ✅ `Wechat pay notify parsed successfully`: 回调解析成功
- ✅ `Trade state received`: 交易状态
- ✅ `Out trade no extracted`: 订单号提取
- ✅ `Starting to update payment status`: 开始更新支付状态
- ✅ `Payment status updated in database successfully`: 支付状态更新成功

### 会员购买日志
- ✅ `handleMembershipPurchase called`: 开始处理会员购买
- ✅ `User info retrieved`: 获取用户信息
- ✅ `Renewing subscription` / `New subscription purchase`: 续费/新购买
- ✅ `User membership updated successfully`: 会员信息更新成功
- ✅ `User membership updated and verified`: 会员信息更新并验证

### 创建卡册日志
- ✅ `User info retrieved`: 获取用户信息
- ✅ `User has active subscription, unlimited book creation`: 订阅会员无限制
- ✅ `User membership expired, cannot create book`: 会员过期（如果过期）

## 测试检查清单

- [ ] 支付订单创建成功
- [ ] 支付记录状态为 `pending`
- [ ] 支付状态成功更新为 `success`
- [ ] 会员类型更新为 `subscription`
- [ ] `is_pro` 更新为 `true`
- [ ] `membership_expires` 设置为正确的到期时间
- [ ] 订阅会员可以无限制创建卡册
- [ ] 过期会员被正确拒绝创建卡册
- [ ] 所有关键步骤都有日志记录
- [ ] 日志信息完整且准确

## 常见问题排查

### 1. 支付状态未更新
- 检查回调是否被调用（查看日志）
- 检查回调解析是否成功
- 检查交易状态是否为 `SUCCESS`
- 检查订单号是否正确

### 2. 会员状态未更新
- 检查 `payment.membership_type` 是否为空
- 检查 `handleMembershipPurchase` 是否被调用
- 查看日志中的错误信息
- 检查数据库更新是否成功

### 3. 创建卡册失败
- 检查会员是否过期
- 检查 `CanUseSubscription()` 返回值
- 查看日志中的权限检查结果
- 检查免费用户是否达到月度限制

## 快速测试命令

```bash
# 一键测试（需要提供TOKEN）
./test_payment_callback_local.sh YOUR_JWT_TOKEN

# 手动触发支付成功（需要提供订单号）
go run test_manual_payment_success.go mem_xxx_xxx

# 查看实时日志
tail -f numind.log | grep -E "pay|membership|book"
```

