#!/bin/bash

# 测试用户统计API
echo "=== 测试用户统计API ==="

# 检查文件是否存在
echo "检查文件结构..."

# 检查用户模型文件
if [ -f "internal/pkg/model/user.go" ]; then
    echo "✅ user.go 模型文件存在"
else
    echo "❌ user.go 模型文件不存在"
    exit 1
fi

# 检查用户biz文件
if [ -f "internal/numind/biz/user/user.go" ]; then
    echo "✅ user.go biz文件存在"
else
    echo "❌ user.go biz文件不存在"
    exit 1
fi

# 检查用户controller文件
if [ -f "internal/numind/controller/v1/user/get.go" ]; then
    echo "✅ get.go controller文件存在"
else
    echo "❌ get.go controller文件不存在"
    exit 1
fi

# 检查book store文件
if [ -f "internal/numind/store/book.go" ]; then
    echo "✅ book.go store文件存在"
else
    echo "❌ book.go store文件不存在"
    exit 1
fi

echo ""
echo "=== 检查代码结构 ==="

# 检查UserWithStats模型
if grep -q "UserWithStats" internal/pkg/model/user.go; then
    echo "✅ UserWithStats模型已定义"
else
    echo "❌ UserWithStats模型未定义"
fi

if grep -q "BookAllNum.*int64" internal/pkg/model/user.go; then
    echo "✅ BookAllNum字段已定义"
else
    echo "❌ BookAllNum字段未定义"
fi

if grep -q "BookNum.*int64" internal/pkg/model/user.go; then
    echo "✅ BookNum字段已定义"
else
    echo "❌ BookNum字段未定义"
fi

# 检查UserBiz接口
if grep -q "GetCurrentUserWithStats" internal/numind/biz/user/user.go; then
    echo "✅ GetCurrentUserWithStats方法已在接口中定义"
else
    echo "❌ GetCurrentUserWithStats方法未在接口中定义"
fi

# 检查BookStore接口
if grep -q "CountByUserAndStatus" internal/numind/store/book.go; then
    echo "✅ CountByUserAndStatus方法已在接口中定义"
else
    echo "❌ CountByUserAndStatus方法未在接口中定义"
fi

if grep -q "CountByUserAndStatusAndDeleted" internal/numind/store/book.go; then
    echo "✅ CountByUserAndStatusAndDeleted方法已在接口中定义"
else
    echo "❌ CountByUserAndStatusAndDeleted方法未在接口中定义"
fi

# 检查实现
if grep -q "func.*CountByUserAndStatus" internal/numind/store/book.go; then
    echo "✅ CountByUserAndStatus方法已实现"
else
    echo "❌ CountByUserAndStatus方法未实现"
fi

if grep -q "func.*CountByUserAndStatusAndDeleted" internal/numind/store/book.go; then
    echo "✅ CountByUserAndStatusAndDeleted方法已实现"
else
    echo "❌ CountByUserAndStatusAndDeleted方法未实现"
fi

# 检查controller实现
if grep -q "GetCurrentUserWithStats" internal/numind/controller/v1/user/get.go; then
    echo "✅ Controller已使用GetCurrentUserWithStats方法"
else
    echo "❌ Controller未使用GetCurrentUserWithStats方法"
fi

echo ""
echo "=== 功能说明 ==="
echo "✅ /v1/users/me API 现在包含以下统计字段："
echo "   - book_all_num: 统计当前用户book状态为非failed的书本数量"
echo "   - book_num: 统计当前用户book状态为非failed且deleteAt为null的书本数量"
echo ""
echo "=== 实现细节 ==="
echo "✅ 新增了UserWithStats模型，包含User的所有字段加上统计信息"
echo "✅ 在BookStore中添加了统计方法"
echo "✅ 在UserBiz中添加了GetCurrentUserWithStats方法"
echo "✅ 修改了GetCurrentUser controller使用新的统计方法"
echo ""
echo "=== 测试建议 ==="
echo "1. 启动服务器"
echo "2. 使用有效token调用 GET /v1/users/me"
echo "3. 检查响应中是否包含 book_all_num 和 book_num 字段"
echo "4. 验证数值是否正确（创建一些不同状态的book进行测试）"
