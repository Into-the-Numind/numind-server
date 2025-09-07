#!/bin/bash

# 支付安全测试脚本
# 用于测试支付API的安全验证机制

BASE_URL="http://localhost:8080"
TOKEN="your_jwt_token_here"  # 需要替换为实际的JWT token

echo "=== 支付安全测试开始 ==="

# 测试1: 正常的订阅会员购买（30天）
echo "测试1: 正常的月度订阅购买"
curl -X POST "$BASE_URL/v1/membership/payment" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "membership_type": "subscription",
    "subscription_days": 30,
    "pay_method": "miniprogram",
    "openid": "test_openid"
  }' | jq '.'

echo -e "\n"

# 测试2: 正常的订阅会员购买（365天）
echo "测试2: 正常的年度订阅购买"
curl -X POST "$BASE_URL/v1/membership/payment" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "membership_type": "subscription",
    "subscription_days": 365,
    "pay_method": "miniprogram",
    "openid": "test_openid"
  }' | jq '.'

echo -e "\n"

# 测试3: 正常的资源包购买（5次）
echo "测试3: 正常的资源包购买"
curl -X POST "$BASE_URL/v1/membership/payment" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "membership_type": "package",
    "package_count": 5,
    "pay_method": "miniprogram",
    "openid": "test_openid"
  }' | jq '.'

echo -e "\n"

# 测试4: 恶意测试 - 无效的订阅天数
echo "测试4: 恶意测试 - 无效的订阅天数（应该失败）"
curl -X POST "$BASE_URL/v1/membership/payment" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "membership_type": "subscription",
    "subscription_days": 100,
    "pay_method": "miniprogram",
    "openid": "test_openid"
  }' | jq '.'

echo -e "\n"

# 测试5: 恶意测试 - 无效的资源包次数
echo "测试5: 恶意测试 - 无效的资源包次数（应该失败）"
curl -X POST "$BASE_URL/v1/membership/payment" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "membership_type": "package",
    "package_count": 10,
    "pay_method": "miniprogram",
    "openid": "test_openid"
  }' | jq '.'

echo -e "\n"

# 测试6: 恶意测试 - 尝试传递价格参数（应该被忽略）
echo "测试6: 恶意测试 - 尝试传递价格参数（应该被忽略）"
curl -X POST "$BASE_URL/v1/membership/payment" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "membership_type": "subscription",
    "subscription_days": 30,
    "amount": 1,
    "pay_method": "miniprogram",
    "openid": "test_openid"
  }' | jq '.'

echo -e "\n"

# 测试7: 恶意测试 - 无效的支付方式
echo "测试7: 恶意测试 - 无效的支付方式（应该失败）"
curl -X POST "$BASE_URL/v1/membership/payment" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "membership_type": "subscription",
    "subscription_days": 30,
    "pay_method": "invalid_method",
    "openid": "test_openid"
  }' | jq '.'

echo -e "\n"

# 测试8: 恶意测试 - 缺少必填参数
echo "测试8: 恶意测试 - 缺少必填参数（应该失败）"
curl -X POST "$BASE_URL/v1/membership/payment" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "membership_type": "subscription",
    "pay_method": "miniprogram",
    "openid": "test_openid"
  }' | jq '.'

echo -e "\n"

# 测试9: 获取会员套餐信息（无需鉴权）
echo "测试9: 获取会员套餐信息"
curl -X GET "$BASE_URL/v1/membership/plans" | jq '.'

echo -e "\n"

# 测试10: 获取用户会员信息
echo "测试10: 获取用户会员信息"
curl -X GET "$BASE_URL/v1/membership/info" \
  -H "Authorization: Bearer $TOKEN" | jq '.'

echo -e "\n"

# 测试11: 检查创建权限
echo "测试11: 检查创建权限"
curl -X GET "$BASE_URL/v1/membership/permission" \
  -H "Authorization: Bearer $TOKEN" | jq '.'

echo -e "\n"

echo "=== 支付安全测试完成 ==="

# 测试结果说明
echo -e "\n=== 测试结果说明 ==="
echo "1. 测试1-3: 应该成功，返回支付信息"
echo "2. 测试4-5: 应该失败，返回参数错误"
echo "3. 测试6: 应该成功，价格参数被忽略，使用服务端计算的价格"
echo "4. 测试7-8: 应该失败，返回参数错误"
echo "5. 测试9: 应该成功，返回套餐信息"
echo "6. 测试10-11: 需要有效token，返回用户信息"
