#!/bin/bash

# API问题诊断脚本
# 用于诊断火山引擎和阿里云API连接问题

set -e

echo "🔍 开始API问题诊断..."
echo "=================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 检查网络连通性
echo -e "${BLUE}📡 检查网络连通性...${NC}"

# 测试DNS解析
if nslookup baidu.com > /dev/null 2>&1; then
    echo -e "${GREEN}✅ DNS解析正常${NC}"
else
    echo -e "${RED}❌ DNS解析失败${NC}"
fi

# 测试基本网络连接
if curl -s --connect-timeout 10 https://www.baidu.com > /dev/null; then
    echo -e "${GREEN}✅ 基本网络连接正常${NC}"
else
    echo -e "${RED}❌ 基本网络连接失败${NC}"
fi

echo ""

# 检查火山引擎API
echo -e "${BLUE}🌋 检查火山引擎API连接...${NC}"
VOLC_URL="https://ark.cn-beijing.volces.com/api/v3"

# 检查域名解析
if nslookup ark.cn-beijing.volces.com > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 火山引擎域名解析正常${NC}"
else
    echo -e "${RED}❌ 火山引擎域名解析失败${NC}"
fi

# 检查连接
if curl -s --connect-timeout 30 -I "$VOLC_URL" > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 火山引擎API连接正常${NC}"
    
    # 获取响应头信息
    response=$(curl -s --connect-timeout 30 -I "$VOLC_URL" 2>/dev/null)
    echo -e "${BLUE}火山引擎响应信息:${NC}"
    echo "$response" | head -5
else
    echo -e "${RED}❌ 火山引擎API连接失败${NC}"
    
    # 尝试详细诊断
    echo -e "${YELLOW}尝试详细诊断...${NC}"
    curl -v --connect-timeout 30 "$VOLC_URL" 2>&1 | head -10 || true
fi

echo ""

# 检查阿里云API
echo -e "${BLUE}☁️ 检查阿里云API连接...${NC}"
ALI_URL="https://dashscope.aliyuncs.com"

# 检查域名解析
if nslookup dashscope.aliyuncs.com > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 阿里云域名解析正常${NC}"
else
    echo -e "${RED}❌ 阿里云域名解析失败${NC}"
fi

# 检查连接
if curl -s --connect-timeout 30 -I "$ALI_URL" > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 阿里云API连接正常${NC}"
    
    # 获取响应头信息
    response=$(curl -s --connect-timeout 30 -I "$ALI_URL" 2>/dev/null)
    echo -e "${BLUE}阿里云响应信息:${NC}"
    echo "$response" | head -5
else
    echo -e "${RED}❌ 阿里云API连接失败${NC}"
    
    # 尝试详细诊断
    echo -e "${YELLOW}尝试详细诊断...${NC}"
    curl -v --connect-timeout 30 "$ALI_URL" 2>&1 | head -10 || true
fi

echo ""

# 检查配置文件
echo -e "${BLUE}⚙️ 检查配置文件...${NC}"

CONFIG_FILES=("config_local.yaml" "config_dev.yaml" "config_prod.yaml" "config_qa.yaml")

for config_file in "${CONFIG_FILES[@]}"; do
    if [ -f "$config_file" ]; then
        echo -e "${GREEN}✅ 找到配置文件: $config_file${NC}"
        
        # 检查火山引擎配置
        if grep -q "volc:" "$config_file"; then
            echo -e "${BLUE}  火山引擎配置:${NC}"
            grep -A 6 "volc:" "$config_file" | grep -E "(api_key|base_url|timeout|max_retries)" || true
        fi
        
        # 检查阿里云配置
        if grep -q "ali:" "$config_file"; then
            echo -e "${BLUE}  阿里云配置:${NC}"
            grep -A 10 "ali:" "$config_file" | grep -E "(api_key|api_url|timeout)" || true
        fi
        
        echo ""
    else
        echo -e "${YELLOW}⚠️ 配置文件不存在: $config_file${NC}"
    fi
done

echo ""

# 检查环境变量
echo -e "${BLUE}🌍 检查环境变量...${NC}"

ENV_VARS=("VOLC_API_KEY" "VOLC_BASE_URL" "ALI_API_KEY" "HTTP_PROXY" "HTTPS_PROXY")

for var in "${ENV_VARS[@]}"; do
    if [ ! -z "${!var}" ]; then
        echo -e "${GREEN}✅ $var 已设置${NC}"
    else
        echo -e "${YELLOW}⚠️ $var 未设置${NC}"
    fi
done

echo ""

# 生成诊断报告
echo -e "${BLUE}📋 诊断建议...${NC}"
echo "=================================="

echo -e "${YELLOW}如果API连接失败，请检查以下项目:${NC}"
echo "1. 网络连接是否正常"
echo "2. 防火墙设置是否阻止了外部连接"
echo "3. API密钥是否正确配置"
echo "4. 超时设置是否合理（建议120秒以上）"
echo "5. 是否需要配置代理服务器"

echo ""
echo -e "${YELLOW}常见解决方案:${NC}"
echo "1. 增加超时时间:"
echo "   volc.timeout: 120s"
echo "   ali.text.timeout: 120s"
echo ""
echo "2. 检查网络代理设置:"
echo "   export HTTP_PROXY=your_proxy_url"
echo "   export HTTPS_PROXY=your_proxy_url"
echo ""
echo "3. 验证API密钥:"
echo "   确保volc.api_key和ali.text.api_key正确"
echo ""
echo "4. 重试机制:"
echo "   volc.max_retries: 5"

echo ""
echo -e "${GREEN}✅ 诊断完成！${NC}"

# 提供快速修复脚本
echo ""
echo -e "${BLUE}🔧 快速修复选项:${NC}"
echo "1. 运行: ./scripts/fix_api_timeout.sh     # 修复超时设置"
echo "2. 运行: ./scripts/test_api_connection.sh # 测试API连接"
echo "3. 查看: tail -f logs/app.log            # 查看实时日志"
