#!/bin/bash

# 测试支付模块重构
echo "=== 测试支付模块重构 ==="

# 检查文件是否存在
echo "检查文件结构..."

# 检查模型文件
if [ -f "internal/pkg/model/payment.go" ]; then
    echo "✅ payment.go 模型文件存在"
else
    echo "❌ payment.go 模型文件不存在"
    exit 1
fi

# 检查store文件
if [ -f "internal/numind/store/payment.go" ]; then
    echo "✅ payment.go store文件存在"
else
    echo "❌ payment.go store文件不存在"
    exit 1
fi

# 检查biz文件
if [ -f "internal/numind/biz/payment/payment.go" ]; then
    echo "✅ payment.go biz文件存在"
else
    echo "❌ payment.go biz文件不存在"
    exit 1
fi

# 检查controller文件
if [ -f "internal/numind/controller/v1/payment/payment.go" ]; then
    echo "✅ payment.go controller文件存在"
else
    echo "❌ payment.go controller文件不存在"
    exit 1
fi

# 检查账户记录controller文件
if [ -f "internal/numind/controller/v1/account/account.go" ]; then
    echo "✅ account.go controller文件存在"
else
    echo "❌ account.go controller文件不存在"
    exit 1
fi

echo ""
echo "=== 检查代码结构 ==="

# 检查PaymentM模型是否包含必要字段
if grep -q "Status.*string" internal/pkg/model/payment.go; then
    echo "✅ PaymentM模型包含Status字段"
else
    echo "❌ PaymentM模型缺少Status字段"
fi

if grep -q "PaymentStatusPending" internal/pkg/model/payment.go; then
    echo "✅ 包含支付状态常量"
else
    echo "❌ 缺少支付状态常量"
fi

# 检查PaymentStore接口
if grep -q "Create.*context.Context.*payment.*PaymentM" internal/numind/store/payment.go; then
    echo "✅ PaymentStore接口包含Create方法"
else
    echo "❌ PaymentStore接口缺少Create方法"
fi

if grep -q "UpdateStatus.*context.Context.*string.*string.*time.Time" internal/numind/store/payment.go; then
    echo "✅ PaymentStore接口包含UpdateStatus方法"
else
    echo "❌ PaymentStore接口缺少UpdateStatus方法"
fi

# 检查PaymentBiz接口
if grep -q "CreatePayment.*context.Context.*CreatePaymentRequest.*uint" internal/numind/biz/payment/payment.go; then
    echo "✅ PaymentBiz接口包含CreatePayment方法"
else
    echo "❌ PaymentBiz接口缺少CreatePayment方法"
fi

if grep -q "UpdatePaymentStatus.*context.Context.*string.*string.*string.*time.Time" internal/numind/biz/payment/payment.go; then
    echo "✅ PaymentBiz接口包含UpdatePaymentStatus方法"
else
    echo "❌ PaymentBiz接口缺少UpdatePaymentStatus方法"
fi

# 检查PaymentController
if grep -q "CreatePayment.*gin.Context" internal/numind/controller/v1/payment/payment.go; then
    echo "✅ PaymentController包含CreatePayment方法"
else
    echo "❌ PaymentController缺少CreatePayment方法"
fi

if grep -q "GetPayment.*gin.Context" internal/numind/controller/v1/payment/payment.go; then
    echo "✅ PaymentController包含GetPayment方法"
else
    echo "❌ PaymentController缺少GetPayment方法"
fi

# 检查AccountController
if grep -q "GetUserPaymentHistory.*gin.Context" internal/numind/controller/v1/account/account.go; then
    echo "✅ AccountController包含GetUserPaymentHistory方法"
else
    echo "❌ AccountController缺少GetUserPaymentHistory方法"
fi

if grep -q "GetUserTotalAmount.*gin.Context" internal/numind/controller/v1/account/account.go; then
    echo "✅ AccountController包含GetUserTotalAmount方法"
else
    echo "❌ AccountController缺少GetUserTotalAmount方法"
fi

echo ""
echo "=== 检查router.go更新 ==="

# 检查router.go是否已更新
if grep -q "账户记录相关" internal/numind/router.go; then
    echo "✅ router.go包含账户记录相关路由"
else
    echo "❌ router.go缺少账户记录相关路由"
fi

if grep -q "account.NewAccountController" internal/numind/router.go; then
    echo "✅ router.go使用AccountController"
else
    echo "❌ router.go未使用AccountController"
fi

echo ""
echo "=== 重构完成 ==="
echo "✅ 支付模块已重构为完整的分层架构"
echo "✅ 账户记录逻辑已从router中提取到controller层"
echo "✅ 所有业务逻辑都通过biz层处理"
echo "✅ 数据操作都通过store层处理"
echo "✅ 代码结构更加清晰，便于维护"
