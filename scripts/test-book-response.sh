#!/bin/bash

echo "测试新的BookResponse结构体"
echo "=========================="

# 测试创建卡册API
echo "1. 测试创建卡册API..."
curl -X POST http://localhost:9091/v1/books \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d '{
    "text": "未来竞争力：联机时代的独立思考与认知进化\n\n---\n\n### 一、联机的独立思考者\n你可以联机打游戏，参考他人的攻略通关，但最终仍需独立完成这一关，达成自己的期待。这恰如跨国企业的\"全球本土化\"（glocal）战略——保持全球视野，同时守护在地特色。若拥有独立思考能力，联机思考将使思想质量更高、迭代更快。这个时代，每个人都需要学会成为\"联机的独立思考者\"。\n\n---\n\n### 二、未来职业的通用竞争力\n在人工智能盛行、行业边界消融的今天，未来的核心竞争力在于：  \n- 用机器学习和处理信息  \n- 用大脑整合并创新思想  \n- 用系统思维解决问题  \n\n每个人都应自问三个关键问题：  \n1. 我今天做的事，机器能做吗？  \n2. 我今天做的事，会被外包吗？  \n3. 我今天做的事，明天会做得更好吗？  \n\n真正的竞争力来自对信息的搜索、思考与趋势洞察能力，而非单纯的知识记忆。\n\n---\n\n### 三、认知方式的跳跃式变革  \n试图记住读过的100本书，如同背诵电话簿才开始拨号——智慧不等于信息囤积。未来认知能力的三大支柱是：  \n1. **搜索能力**：快速定位有效信息  \n2. **思考能力**：将信息转化为洞见  \n3. **洞察能力**：从海量数据中捕捉趋势  \n\n人类用两千年建立的\"记忆知识\"体系，在近20年内被颠覆。这种认知方式的转变并非渐进，而是不连续的、革命性的跳跃。记忆应交给电脑，大脑则专注创造性的整合与突破。\n\n---\n\n（注：原文中的页码编号及重复章节标题已按规则过滤）",
    "template_id": "1"
  }' | jq '.'

echo ""
echo "2. 获取创建的书籍详情..."
echo "请使用上面返回的id来获取详情："
echo "curl -X GET http://localhost:9091/v1/books/{book_id} -H \"Authorization: Bearer YOUR_TOKEN_HERE\" | jq '.'"

echo ""
echo "3. 检查数据库中的卡片数据..."
echo "SELECT book_id, card_count, LENGTH(processed_text) as text_length FROM books WHERE id = {book_id};"
echo "SELECT id, book_id, LENGTH(processed_text) as text_length FROM cards WHERE book_id = {book_id};"

echo ""
echo "预期返回的JSON结构："
echo '{
  "code": 0,
  "message": "",
  "data": {
    "id": 16,
    "created_at": "2025-08-04T17:16:22.95+08:00",
    "updated_at": "2025-08-04T17:16:22.96+08:00",
    "deleted_at": null,
    "user_id": 1,
    "title": "AI生成卡册 - 2025-08-04 17:16:22",
    "category_id": null,
    "category_name": "",
    "template_id": "1",
    "tags": "",
    "card_count": 3,
    "view_time": "2025-08-04T17:16:22.944145+08:00",
    "cards": [
      {
        "id": 1,
        "created_at": "2025-08-04T17:16:22.95+08:00",
        "updated_at": "2025-08-04T17:16:22.96+08:00",
        "deleted_at": null,
        "user_id": 1,
        "book_id": 16,
        "image_id": 0,
        "processed_text": [
          [
            {
              "type": "number",
              "content": "AI处理结果"
            },
            {
              "type": "body",
              "content": "未来竞争力：联机时代的独立思考与认知进化..."
            }
          ]
        ],
        "sort_order": 0,
        "tags": ""
      }
    ]
  }
}'

echo ""
echo "测试完成！" 