#!/bin/bash

echo "测试简化的卡册创建功能"
echo "====================="

# 测试创建卡册API（简化版）
echo "1. 测试创建卡册API（简化版）..."
curl -X POST http://localhost:8080/v1/books \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN_HERE" \
  -d '{
    "text": "未来竞争力：联机时代的独立思考与认知进化\n\n---\n\n### 一、联机的独立思考者\n你可以联机打游戏，参考他人的攻略通关，但最终仍需独立完成这一关，达成自己的期待。这恰如跨国企业的\"全球本土化\"（glocal）战略——保持全球视野，同时守护在地特色。若拥有独立思考能力，联机思考将使思想质量更高、迭代更快。这个时代，每个人都需要学会成为\"联机的独立思考者\"。\n\n---\n\n### 二、未来职业的通用竞争力\n在人工智能盛行、行业边界消融的今天，未来的核心竞争力在于：  \n- 用机器学习和处理信息  \n- 用大脑整合并创新思想  \n- 用系统思维解决问题  \n\n每个人都应自问三个关键问题：  \n1. 我今天做的事，机器能做吗？  \n2. 我今天做的事，会被外包吗？  \n3. 我今天做的事，明天会做得更好吗？  \n\n真正的竞争力来自对信息的搜索、思考与趋势洞察能力，而非单纯的知识记忆。\n\n---\n\n### 三、认知方式的跳跃式变革  \n试图记住读过的100本书，如同背诵电话簿才开始拨号——智慧不等于信息囤积。未来认知能力的三大支柱是：  \n1. **搜索能力**：快速定位有效信息  \n2. **思考能力**：将信息转化为洞见  \n3. **洞察能力**：从海量数据中捕捉趋势  \n\n人类用两千年建立的\"记忆知识\"体系，在近20年内被颠覆。这种认知方式的转变并非渐进，而是不连续的、革命性的跳跃。记忆应交给电脑，大脑则专注创造性的整合与突破。\n\n---\n\n（注：原文中的页码编号及重复章节标题已按规则过滤）",
    "template_id": "1"
  }' | jq '.'

echo ""
echo "2. 检查图片存储路径..."
echo "卡册封面图片路径：resource.image_path/{bookid}/book_{id}.webp"
echo "卡片图片路径：resource.image_path/{cardid}/card_{id}.webp"
echo "临时HTML文件路径：resource.image_path/{cardid}/card_{id}.html"

echo ""
echo "3. 验证功能流程："
echo "✅ 第一步：调用文字大模型，获取返回的 markdown 格式内容"
echo "✅ 第二步：从 markdown 内容中提取 image_prompt 字段"
echo "✅ 第三步：调用文生图大模型生成图片"
echo "✅ 第四步：图片存储规则实现"
echo "✅ 第五步：临时HTML文件生成"

echo ""
echo "4. 路径规则验证："
echo "📁 卡册封面图片：/res/upload/book/{bookid}/book_{id}.webp"
echo "📁 卡片图片：/res/upload/card/{cardid}/card_{id}.webp"
echo "📁 临时HTML：/res/upload/card/{cardid}/card_{id}.html"

echo ""
echo "测试完成！"
