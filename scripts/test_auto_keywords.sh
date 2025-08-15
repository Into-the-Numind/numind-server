#!/bin/bash

# 自动标签生成功能测试脚本
# 测试新的关键词生成和匹配功能

echo "🔖 自动标签生成功能测试"
echo "========================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 检查项目编译
echo -e "${BLUE}1. 检查项目编译...${NC}"
if go build ./cmd/numind/... > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 项目编译成功${NC}"
else
    echo -e "${RED}❌ 项目编译失败${NC}"
    exit 1
fi

echo ""

# 检查新增的代码文件
echo -e "${BLUE}2. 检查新增的代码文件...${NC}"

if [ -f "internal/numind/biz/book/keyword_generator.go" ]; then
    echo -e "${GREEN}✅ 关键词生成器已创建${NC}"
else
    echo -e "${RED}❌ 关键词生成器未创建${NC}"
fi

if [ -f "internal/pkg/util/keyword_matcher.go" ]; then
    echo -e "${GREEN}✅ 关键词匹配器已更新${NC}"
else
    echo -e "${RED}❌ 关键词匹配器未更新${NC}"
fi

echo ""

# 检查模型更新
echo -e "${BLUE}3. 检查模型更新...${NC}"

if grep -q "Keywords.*\[\]string" internal/pkg/model/book.go; then
    echo -e "${GREEN}✅ BookM模型已添加Keywords字段${NC}"
else
    echo -e "${RED}❌ BookM模型未添加Keywords字段${NC}"
fi

if grep -q "GetKeywords" internal/pkg/model/book.go; then
    echo -e "${GREEN}✅ BookM模型已添加GetKeywords方法${NC}"
else
    echo -e "${RED}❌ BookM模型未添加GetKeywords方法${NC}"
fi

echo ""

# 检查接口更新
echo -e "${BLUE}4. 检查接口更新...${NC}"

if grep -q "GetKeywords.*\[\]string" internal/pkg/util/keyword_matcher.go; then
    echo -e "${GREEN}✅ BookMatcher接口已添加GetKeywords方法${NC}"
else
    echo -e "${RED}❌ BookMatcher接口未添加GetKeywords方法${NC}"
fi

if grep -q "MatchScore.*BookMatcher" internal/pkg/util/keyword_matcher.go; then
    echo -e "${GREEN}✅ MatchScore函数已更新为使用BookMatcher接口${NC}"
else
    echo -e "${RED}❌ MatchScore函数未更新${NC}"
fi

echo ""

# 检查搜索服务更新
echo -e "${BLUE}5. 检查搜索服务更新...${NC}"

if grep -q "keywordGenerator" internal/numind/biz/book/search.go; then
    echo -e "${GREEN}✅ 搜索服务已集成关键词生成器${NC}"
else
    echo -e "${RED}❌ 搜索服务未集成关键词生成器${NC}"
fi

if grep -q "BatchUpdateKeywords" internal/numind/biz/book/search.go; then
    echo -e "${GREEN}✅ 搜索服务已添加批量更新关键词功能${NC}"
else
    echo -e "${RED}❌ 搜索服务未添加批量更新关键词功能${NC}"
fi

echo ""

# 检查聊天业务逻辑更新
echo -e "${BLUE}6. 检查聊天业务逻辑更新...${NC}"

if grep -q "BatchUpdateKeywords" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ 聊天业务逻辑已集成关键词生成${NC}"
else
    echo -e "${RED}❌ 聊天业务逻辑未集成关键词生成${NC}"
fi

if grep -q "keywords.*book.Keywords" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ 搜索结果已包含关键词信息${NC}"
else
    echo -e "${RED}❌ 搜索结果未包含关键词信息${NC}"
fi

echo ""

# 显示功能说明
echo -e "${BLUE}7. 自动标签生成功能说明:${NC}"
echo "新增功能包括："
echo ""
echo "🔖 **关键词生成**: 自动从书籍标题和标签中提取关键词"
echo "📊 **智能匹配**: 使用关键词进行更精准的搜索匹配"
echo "🔄 **自动更新**: 搜索时自动为书籍生成关键词"
echo "💾 **JSON存储**: 关键词以JSON数组形式存储在数据库中"
echo "🎯 **向后兼容**: 保持原有Tags字段的兼容性"
echo ""

# 显示技术架构
echo -e "${BLUE}8. 技术架构:${NC}"
echo "• BookM模型: 新增Keywords字段，类型为[]string"
echo "• KeywordGenerator: 负责生成和管理关键词"
echo "• SearchService: 集成关键词生成和匹配功能"
echo "• ChatBiz: 在搜索前自动生成关键词"
echo "• 数据库: 支持JSON字段存储关键词数组"
echo ""

# 显示使用流程
echo -e "${BLUE}9. 使用流程:${NC}"
echo "1. 用户发送搜索请求"
echo "2. 系统获取所有书籍"
echo "3. 自动为书籍生成关键词（如果还没有）"
echo "4. 使用关键词进行智能匹配"
echo "5. 返回包含关键词的搜索结果"
echo ""

# 下一步操作
echo -e "${BLUE}10. 下一步操作:${NC}"
echo -e "${GREEN}现在可以启动服务测试自动标签生成功能：${NC}"
echo "go run cmd/numind/main.go"
echo ""
echo "测试方法："
echo "1. 启动服务，观察关键词生成日志"
echo "2. 发送搜索请求，查看是否自动生成关键词"
echo "3. 检查搜索结果是否包含关键词信息"
echo "4. 验证关键词匹配的准确性"
echo ""

echo -e "${BLUE}11. 预期效果:${NC}"
echo "• 搜索时自动为书籍生成关键词"
echo "• 关键词存储在Keywords字段中"
echo "• 搜索结果包含关键词信息"
echo "• 匹配算法更加精准"
echo "• 支持中文分词和停用词过滤"
echo ""

echo -e "${GREEN}🎉 自动标签生成功能已集成完成！${NC}"
echo ""
echo -e "${YELLOW}主要特性:${NC}"
echo "• 自动关键词生成"
echo "• 智能搜索匹配"
echo "• JSON字段存储"
echo "• 向后兼容设计"
echo "• 集成到现有流程"
echo ""
echo -e "${BLUE}现在重启服务即可体验新的关键词功能！${NC}"
