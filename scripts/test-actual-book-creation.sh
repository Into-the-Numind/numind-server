#!/bin/bash

# 实际Book创建功能测试脚本
# 模拟真实的API请求来测试修复后的功能

echo "=== 实际Book创建功能测试 ==="

# 检查服务是否运行
echo "检查服务状态..."
if ! curl -s http://localhost:9091/health > /dev/null 2>&1; then
    echo "❌ 服务未运行，请先启动服务"
    echo "启动命令: go run cmd/numind/main.go"
    exit 1
fi

echo "✅ 服务正在运行"

# 测试数据：模拟用户的长文本输入
TEST_TEXT="我好像发现了魅力的本质! 1.深度的自我接纳 魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点，而是清醒认知自身的优势与局限后，既不刻意放大优点去炫耀，也不因短板而自我否定。比如一个人坦然承认自己内向不善社交，却能在独处时展现出专注的思考力，这种真实反而比刻意扮演外向更有吸引力，传递出我不需要通过伪装获得认可的笃定，让接触者感到轻松无压力。 2.稳定的情绪内核 情绪稳定并非毫无波澜，而是在面对突发状况、负面评价或生活起伏时，能快速调整状态，不被情绪牵着走。比如职场中遇到突发失误，有人慌乱指责，有人却能先冷静梳理问题、提出解决方案，这种泰山崩于前而色不变的定力，会让人产生强烈的信赖感，人们潜意识里更愿意靠近能提供情绪支撑的人，而非成为他人的情绪垃圾桶。 3.流动的内在丰盈 内在丰盈不是死记硬背的知识堆砌，而是将经历、思考、兴趣内化成一种感知力。比如一个热爱生活的人，能从路边落叶联想到季节的诗意，从日常对话中捕捉到人性的细节，言谈间既有对专业领域的深刻见解，也有对生活琐事的细腻观察。这种肚子里有东西的状态，会让人觉得与他相处永远有新的发现，像一本常读常新的书。"

# 测试1: 创建book（使用template_id=3）
echo -e "\n=== 测试1: 创建book（使用template_id=3） ==="
echo "发送创建book请求..."

RESPONSE=$(curl -s -X POST 'http://localhost:9091/v1/books' \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTUyMjY4ODYsIm9wZW5pZCI6IjY2NiIsInVzZXJfaWQiOjJ9.7KKg_euDf6vgYxhfbJPE7v0u9e8MlHpE72H3CoBTBEQ' \
  -d "{
    \"text\": \"$TEST_TEXT\",
    \"template_id\": \"3\"
  }")

echo "响应状态码: $?"
echo "响应内容:"
echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"

# 提取book_id
BOOK_ID=$(echo "$RESPONSE" | jq -r '.data.id' 2>/dev/null)
if [ "$BOOK_ID" != "null" ] && [ "$BOOK_ID" != "" ]; then
    echo "✅ Book创建成功，ID: $BOOK_ID"
    
    # 等待一段时间让异步处理完成
    echo "等待异步处理完成..."
    sleep 10
    
    # 测试2: 查询book详情
    echo -e "\n=== 测试2: 查询book详情 ==="
    BOOK_DETAIL=$(curl -s -X GET "http://localhost:9091/v1/books/$BOOK_ID" \
      -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTUyMjY4ODYsIm9wZW5pZCI6IjY2NiIsInVzZXJfaWQiOjJ9.7KKg_euDf6vgYxhfbJPE7v0u9e8MlHpE72H3CoBTBEQ')
    
    echo "Book详情:"
    echo "$BOOK_DETAIL" | jq '.' 2>/dev/null || echo "$BOOK_DETAIL"
    
    # 测试3: 查询book的卡片
    echo -e "\n=== 测试3: 查询book的卡片 ==="
    CARDS_RESPONSE=$(curl -s -X GET "http://localhost:9091/v1/books/$BOOK_ID/cards" \
      -H 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTUyMjY4ODYsIm9wZW5pZCI6IjY2NiIsInVzZXJfaWQiOjJ9.7KKg_euDf6vgYxhfbJPE7v0u9e8MlHpE72H3CoBTBEQ')
    
    echo "卡片列表:"
    echo "$CARDS_RESPONSE" | jq '.' 2>/dev/null || echo "$CARDS_RESPONSE"
    
    # 分析结果
    echo -e "\n=== 结果分析 ==="
    
    # 检查是否有封面卡片
    COVER_CARD=$(echo "$CARDS_RESPONSE" | jq -r '.data[] | select(.sort_order == 0)' 2>/dev/null)
    if [ "$COVER_CARD" != "null" ] && [ "$COVER_CARD" != "" ]; then
        echo "✅ 封面卡片创建成功"
        COVER_BACKGROUND=$(echo "$COVER_CARD" | jq -r '.processed_text' 2>/dev/null | jq -r '.[] | select(.type == "background") | .content' 2>/dev/null)
        if [ "$COVER_BACKGROUND" != "null" ] && [ "$COVER_BACKGROUND" != "" ]; then
            echo "✅ 模板背景应用成功: $COVER_BACKGROUND"
        else
            echo "⚠️  模板背景未应用"
        fi
    else
        echo "❌ 封面卡片创建失败"
    fi
    
    # 检查内容卡片
    CONTENT_CARDS=$(echo "$CARDS_RESPONSE" | jq -r '.data[] | select(.sort_order > 0)' 2>/dev/null)
    if [ "$CONTENT_CARDS" != "null" ] && [ "$CONTENT_CARDS" != "" ]; then
        CARD_COUNT=$(echo "$CARDS_RESPONSE" | jq -r '.data[] | select(.sort_order > 0) | .id' 2>/dev/null | wc -l)
        echo "✅ 内容卡片创建成功，数量: $CARD_COUNT"
        
        # 检查分页效果
        FIRST_CARD_CONTENT=$(echo "$CARDS_RESPONSE" | jq -r '.data[] | select(.sort_order == 1) | .processed_text' 2>/dev/null)
        if [ "$FIRST_CARD_CONTENT" != "null" ] && [ "$FIRST_CARD_CONTENT" != "" ]; then
            CONTENT_LENGTH=$(echo "$FIRST_CARD_CONTENT" | jq -r '.[] | .content' 2>/dev/null | wc -c)
            echo "✅ 第一张内容卡片内容长度: $CONTENT_LENGTH 字符"
        fi
    else
        echo "❌ 内容卡片创建失败"
    fi
    
else
    echo "❌ Book创建失败"
    echo "错误信息:"
    echo "$RESPONSE" | jq -r '.message' 2>/dev/null || echo "无法解析错误信息"
fi

echo -e "\n=== 测试完成 ==="
