#!/bin/bash

# 卡册创建与无头浏览器渲染器集成测试脚本

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
API_BASE_URL="http://localhost:9091/api/v1"
TEST_TEXT="联机时代的独立思考者：未来竞争力的进化之路

在这个信息爆炸的时代，独立思考能力变得越来越重要。我们需要学会在纷繁复杂的信息中筛选出有价值的内容，形成自己的判断和观点。

未来职业竞争力的关键要素：
• 我今天做的事，机器能做吗？
• 我今天做的事，会被外包吗？
• 我今天做的事，明天会做得更好吗？

真正的智慧不在于知道答案，而在于知道如何提问。"

echo -e "${BLUE}=== 卡册创建与无头浏览器渲染器集成测试 ===${NC}"

# 1. 检查服务器是否运行
echo -e "\n${YELLOW}1. 检查服务器状态...${NC}"
if curl -s "${API_BASE_URL}/healthz" > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 服务器运行正常${NC}"
else
    echo -e "${RED}❌ 服务器未运行，请先启动服务器${NC}"
    echo -e "${YELLOW}启动命令: go run cmd/numind/main.go -c config_local.yaml${NC}"
    exit 1
fi

# 2. 创建测试卡册
echo -e "\n${YELLOW}2. 创建测试卡册...${NC}"
CREATE_RESPONSE=$(curl -s -X POST "$API_BASE_URL/books" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d "{
    \"text\": \"$TEST_TEXT\",
    \"template_id\": \"1\"
  }")

echo "创建卡册响应:"
echo "$CREATE_RESPONSE" | jq '.' 2>/dev/null || echo "$CREATE_RESPONSE"

# 提取book_id
BOOK_ID=$(echo "$CREATE_RESPONSE" | jq -r '.data.id' 2>/dev/null)
echo -e "${BLUE}创建的book_id: $BOOK_ID${NC}"

if [ "$BOOK_ID" = "null" ] || [ -z "$BOOK_ID" ] || [ "$BOOK_ID" = "" ]; then
    echo -e "${RED}❌ 创建卡册失败${NC}"
    exit 1
fi

echo -e "${GREEN}✅ 创建卡册成功${NC}"

# 3. 获取卡册详情
echo -e "\n${YELLOW}3. 获取卡册详情...${NC}"
BOOK_DETAIL_RESPONSE=$(curl -s -X GET "$API_BASE_URL/books/$BOOK_ID" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE")

echo "卡册详情响应:"
echo "$BOOK_DETAIL_RESPONSE" | jq '.' 2>/dev/null || echo "$BOOK_DETAIL_RESPONSE"

# 检查卡片数量
CARD_COUNT=$(echo "$BOOK_DETAIL_RESPONSE" | jq -r '.data.cards | length' 2>/dev/null)
echo -e "${BLUE}卡片数量: $CARD_COUNT${NC}"

# 检查渲染图片数量
RENDERED_CARDS=$(echo "$BOOK_DETAIL_RESPONSE" | jq -r '.data.cards[] | select(.rendered_image != null and .rendered_image != "") | .id' 2>/dev/null)
RENDERED_COUNT=$(echo "$RENDERED_CARDS" | wc -l)

echo -e "${BLUE}已渲染卡片数量: $RENDERED_COUNT${NC}"

if [ "$RENDERED_COUNT" -gt 0 ]; then
    echo -e "${GREEN}✅ 无头浏览器渲染器工作正常${NC}"
    echo -e "${BLUE}渲染的卡片ID: $RENDERED_CARDS${NC}"
else
    echo -e "${YELLOW}⚠️  没有检测到渲染的卡片，可能的原因：${NC}"
    echo -e "${YELLOW}   - 渲染过程是异步的，需要等待${NC}"
    echo -e "${YELLOW}   - Chrome浏览器未安装${NC}"
    echo -e "${YELLOW}   - 渲染过程中出现错误${NC}"
fi

# 4. 检查图片文件
echo -e "\n${YELLOW}4. 检查生成的图片文件...${NC}"
if [ "$RENDERED_COUNT" -gt 0 ]; then
    for card_id in $RENDERED_CARDS; do
        IMAGE_PATH="./images/upload/card/$card_id/card_$card_id.png"
        if [ -f "$IMAGE_PATH" ]; then
            echo -e "${GREEN}✅ 卡片 $card_id 图片文件存在: $IMAGE_PATH${NC}"
            echo -e "${BLUE}   文件大小: $(ls -lh "$IMAGE_PATH" | awk '{print $5}')${NC}"
        else
            echo -e "${RED}❌ 卡片 $card_id 图片文件不存在: $IMAGE_PATH${NC}"
        fi
    done
else
    echo -e "${YELLOW}⚠️  没有渲染的卡片，跳过图片文件检查${NC}"
fi

# 5. 测试手动渲染API
echo -e "\n${YELLOW}5. 测试手动渲染API...${NC}"
if [ "$CARD_COUNT" -gt 0 ]; then
    FIRST_CARD_ID=$(echo "$BOOK_DETAIL_RESPONSE" | jq -r '.data.cards[0].id' 2>/dev/null)
    if [ "$FIRST_CARD_ID" != "null" ] && [ -n "$FIRST_CARD_ID" ]; then
        echo -e "${BLUE}测试渲染卡片ID: $FIRST_CARD_ID${NC}"
        
        RENDER_RESPONSE=$(curl -s -X POST "$API_BASE_URL/cards/render" \
          -H "Content-Type: application/json" \
          -H "Authorization: Bearer YOUR_TOKEN_HERE" \
          -d "{
            \"card_id\": $FIRST_CARD_ID
          }")
        
        echo "手动渲染响应:"
        echo "$RENDER_RESPONSE" | jq '.' 2>/dev/null || echo "$RENDER_RESPONSE"
        
        MANUAL_RENDERED=$(echo "$RENDER_RESPONSE" | jq -r '.data.image_url' 2>/dev/null)
        if [ "$MANUAL_RENDERED" != "null" ] && [ -n "$MANUAL_RENDERED" ]; then
            echo -e "${GREEN}✅ 手动渲染成功${NC}"
        else
            echo -e "${RED}❌ 手动渲染失败${NC}"
        fi
    else
        echo -e "${YELLOW}⚠️  无法获取第一个卡片ID${NC}"
    fi
else
    echo -e "${YELLOW}⚠️  没有卡片，跳过手动渲染测试${NC}"
fi

# 6. 总结
echo -e "\n${BLUE}=== 测试总结 ===${NC}"
echo -e "${BLUE}卡册ID: $BOOK_ID${NC}"
echo -e "${BLUE}卡片总数: $CARD_COUNT${NC}"
echo -e "${BLUE}渲染卡片数: $RENDERED_COUNT${NC}"

if [ "$RENDERED_COUNT" -gt 0 ]; then
    echo -e "${GREEN}✅ 无头浏览器渲染器集成成功！${NC}"
    echo -e "${GREEN}✅ 卡册创建与渲染功能正常工作${NC}"
else
    echo -e "${YELLOW}⚠️  渲染功能可能需要进一步检查${NC}"
    echo -e "${YELLOW}请检查：${NC}"
    echo -e "${YELLOW}  1. Chrome浏览器是否已安装${NC}"
    echo -e "${YELLOW}  2. 服务器日志中是否有错误信息${NC}"
    echo -e "${YELLOW}  3. 图片保存路径是否正确${NC}"
fi

echo -e "\n${BLUE}测试完成${NC}" 