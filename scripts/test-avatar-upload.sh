#!/bin/bash

# 测试头像上传功能
# 使用方法: ./test-avatar-upload.sh <token> <image_file>

if [ $# -ne 2 ]; then
    echo "使用方法: $0 <token> <image_file>"
    echo "示例: $0 'your-jwt-token' 'avatar.jpg'"
    exit 1
fi

TOKEN=$1
IMAGE_FILE=$2

# 检查文件是否存在
if [ ! -f "$IMAGE_FILE" ]; then
    echo "错误: 文件 '$IMAGE_FILE' 不存在"
    exit 1
fi

# 检查文件类型
FILE_EXT=$(echo "$IMAGE_FILE" | tr '[:upper:]' '[:lower:]' | sed 's/.*\.//')
if [[ ! "$FILE_EXT" =~ ^(jpg|jpeg|png|gif)$ ]]; then
    echo "错误: 不支持的文件格式 '$FILE_EXT'，只支持 jpg, jpeg, png, gif"
    exit 1
fi

# 检查文件大小（2MB限制）
FILE_SIZE=$(stat -f%z "$IMAGE_FILE" 2>/dev/null || stat -c%s "$IMAGE_FILE" 2>/dev/null)
if [ "$FILE_SIZE" -gt 2097152 ]; then
    echo "错误: 文件大小超过2MB限制"
    exit 1
fi

echo "开始测试头像上传..."
echo "文件: $IMAGE_FILE"
echo "大小: $FILE_SIZE bytes"
echo "类型: $FILE_EXT"

# 发送请求
RESPONSE=$(curl -s -X POST \
    -H "Authorization: Bearer $TOKEN" \
    -F "avatar=@$IMAGE_FILE" \
    "http://localhost:9091/v1/users/avatar")

echo "响应:"
echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"

echo ""
echo "测试完成" 