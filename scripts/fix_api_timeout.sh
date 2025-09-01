#!/bin/bash

# API超时修复脚本
# 自动修复常见的API超时和配置问题

set -e

echo "🔧 开始修复API超时问题..."
echo "=================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 备份配置文件
echo -e "${BLUE}📦 备份现有配置文件...${NC}"

CONFIG_FILES=("config_local.yaml" "config_dev.yaml" "config_prod.yaml" "config_qa.yaml")

for config_file in "${CONFIG_FILES[@]}"; do
    if [ -f "$config_file" ]; then
        cp "$config_file" "${config_file}.backup.$(date +%Y%m%d_%H%M%S)"
        echo -e "${GREEN}✅ 已备份: $config_file${NC}"
    fi
done

echo ""

# 修复volc超时设置
echo -e "${BLUE}🌋 修复火山引擎超时设置...${NC}"

fix_volc_timeout() {
    local config_file="$1"
    
    if [ ! -f "$config_file" ]; then
        echo -e "${YELLOW}⚠️ 配置文件不存在: $config_file${NC}"
        return
    fi
    
    echo -e "${BLUE}处理文件: $config_file${NC}"
    
    # 检查并修复volc timeout
    if grep -q "volc:" "$config_file"; then
        # 如果timeout设置为30s或更少，改为120s
        sed -i.tmp 's/timeout: [0-9]*s/timeout: 120s/g' "$config_file"
        sed -i.tmp 's/timeout: [12][0-9]s/timeout: 120s/g' "$config_file"
        
        # 如果没有max_retries，添加它
        if ! grep -A 10 "volc:" "$config_file" | grep -q "max_retries:"; then
            sed -i.tmp '/volc:/,/^[^ ]/ s/\(  timeout: 120s.*\)/\1\n  max_retries: 5/' "$config_file"
        else
            # 更新max_retries为5
            sed -i.tmp 's/max_retries: [0-9]*/max_retries: 5/g' "$config_file"
        fi
        
        echo -e "${GREEN}✅ 已修复火山引擎超时设置${NC}"
    else
        echo -e "${YELLOW}⚠️ 未找到volc配置${NC}"
    fi
    
    # 清理临时文件
    rm -f "${config_file}.tmp"
}

# 修复阿里云超时设置
fix_ali_timeout() {
    local config_file="$1"
    
    if [ ! -f "$config_file" ]; then
        return
    fi
    
    # 修复ali text timeout
    if grep -q "ali:" "$config_file"; then
        # 为text配置添加timeout
        if grep -A 5 "text:" "$config_file" | grep -q "timeout:"; then
            sed -i.tmp 's/\(text:.*timeout:\) [0-9]*s/\1 120s/g' "$config_file"
        else
            # 在text配置中添加timeout
            sed -i.tmp '/text:/,/image:/ s/\(    model: .*\)/\1\n    timeout: 120s/' "$config_file"
        fi
        
        # 为image配置添加timeout
        if grep -A 5 "image:" "$config_file" | grep -q "timeout:"; then
            sed -i.tmp 's/\(image:.*timeout:\) [0-9]*s/\1 180s/g' "$config_file"
        else
            # 在image配置中添加timeout
            sed -i.tmp '/image:/,/^[^ ]/ s/\(    model: .*\)/\1\n    timeout: 180s/' "$config_file"
        fi
        
        echo -e "${GREEN}✅ 已修复阿里云超时设置${NC}"
    fi
    
    # 清理临时文件
    rm -f "${config_file}.tmp"
}

# 应用修复到所有配置文件
for config_file in "${CONFIG_FILES[@]}"; do
    if [ -f "$config_file" ]; then
        fix_volc_timeout "$config_file"
        fix_ali_timeout "$config_file"
    fi
done

echo ""

# 验证修复结果
echo -e "${BLUE}🔍 验证修复结果...${NC}"

for config_file in "${CONFIG_FILES[@]}"; do
    if [ -f "$config_file" ]; then
        echo -e "${BLUE}检查文件: $config_file${NC}"
        
        # 检查volc配置
        if grep -A 6 "volc:" "$config_file" | grep -q "timeout: 120s"; then
            echo -e "${GREEN}  ✅ 火山引擎超时: 120s${NC}"
        else
            echo -e "${RED}  ❌ 火山引擎超时设置可能有问题${NC}"
        fi
        
        if grep -A 6 "volc:" "$config_file" | grep -q "max_retries: 5"; then
            echo -e "${GREEN}  ✅ 火山引擎重试次数: 5${NC}"
        else
            echo -e "${YELLOW}  ⚠️ 火山引擎重试次数可能需要检查${NC}"
        fi
        
        # 检查ali配置
        if grep -A 10 "ali:" "$config_file" | grep -q "timeout: 120s"; then
            echo -e "${GREEN}  ✅ 阿里云文本超时: 120s${NC}"
        else
            echo -e "${YELLOW}  ⚠️ 阿里云文本超时可能需要检查${NC}"
        fi
        
        if grep -A 10 "ali:" "$config_file" | grep -q "timeout: 180s"; then
            echo -e "${GREEN}  ✅ 阿里云图像超时: 180s${NC}"
        else
            echo -e "${YELLOW}  ⚠️ 阿里云图像超时可能需要检查${NC}"
        fi
        
        echo ""
    fi
done

echo ""

# 创建增强版HTTP客户端配置
echo -e "${BLUE}⚙️ 创建增强版HTTP客户端配置...${NC}"

cat > httpclient_enhanced_config.yaml << 'EOF'
# 增强版HTTP客户端配置
# 用于解决API连接超时问题

httpclient:
  default:
    timeout: 120s
    connect_timeout: 30s
    response_header_timeout: 120s
    tls_handshake_timeout: 10s
    idle_conn_timeout: 90s
    max_idle_conns: 100
    max_retries: 5
    retry_delay: 2s
    retry_backoff: 2.0
    enable_compression: true
    user_agent: "numind-server/1.0"

  volc:
    timeout: 120s
    connect_timeout: 30s
    response_header_timeout: 120s
    max_retries: 5
    retry_delay: 3s
    retry_backoff: 2.0

  ali:
    timeout: 120s
    connect_timeout: 30s
    response_header_timeout: 120s
    max_retries: 5
    retry_delay: 2s
    retry_backoff: 2.0

# 网络代理配置（如果需要）
# proxy:
#   http_proxy: "http://your-proxy:port"
#   https_proxy: "https://your-proxy:port"
#   no_proxy: "localhost,127.0.0.1"
EOF

echo -e "${GREEN}✅ 已创建增强版HTTP客户端配置: httpclient_enhanced_config.yaml${NC}"

echo ""

# 生成环境变量脚本
echo -e "${BLUE}🌍 生成环境变量设置脚本...${NC}"

cat > set_api_env.sh << 'EOF'
#!/bin/bash

# API环境变量设置脚本
# 设置优化的HTTP客户端参数

echo "设置API优化环境变量..."

# HTTP客户端优化
export HTTP_CLIENT_TIMEOUT=120s
export HTTP_CLIENT_MAX_RETRIES=5
export HTTP_CLIENT_RETRY_DELAY=2s

# 火山引擎API优化
export VOLC_TIMEOUT=120s
export VOLC_MAX_RETRIES=5
export VOLC_RETRY_DELAY=3s

# 阿里云API优化
export ALI_TIMEOUT=120s
export ALI_MAX_RETRIES=5
export ALI_RETRY_DELAY=2s

# 网络优化
export GOMEMLIMIT=1GiB
export GOGC=100

# 如果需要代理，取消注释以下行
# export HTTP_PROXY=your_proxy_url
# export HTTPS_PROXY=your_proxy_url

echo "环境变量设置完成！"
echo "使用方法: source set_api_env.sh"
EOF

chmod +x set_api_env.sh
echo -e "${GREEN}✅ 已创建环境变量脚本: set_api_env.sh${NC}"

echo ""

# 生成测试脚本
echo -e "${BLUE}🧪 生成API连接测试脚本...${NC}"

cat > test_api_connection.sh << 'EOF'
#!/bin/bash

# API连接测试脚本

echo "🧪 测试API连接..."

# 测试火山引擎API
echo "测试火山引擎API连接..."
timeout 30 curl -s -I https://ark.cn-beijing.volces.com/api/v3 && echo "✅ 火山引擎连接正常" || echo "❌ 火山引擎连接失败"

# 测试阿里云API
echo "测试阿里云API连接..."
timeout 30 curl -s -I https://dashscope.aliyuncs.com && echo "✅ 阿里云连接正常" || echo "❌ 阿里云连接失败"

# 测试DNS解析
echo "测试DNS解析..."
nslookup ark.cn-beijing.volces.com > /dev/null && echo "✅ 火山引擎DNS正常" || echo "❌ 火山引擎DNS失败"
nslookup dashscope.aliyuncs.com > /dev/null && echo "✅ 阿里云DNS正常" || echo "❌ 阿里云DNS失败"

echo "测试完成！"
EOF

chmod +x test_api_connection.sh
echo -e "${GREEN}✅ 已创建连接测试脚本: test_api_connection.sh${NC}"

echo ""

# 修复完成
echo -e "${GREEN}✅ API超时修复完成！${NC}"
echo "=================================="

echo -e "${YELLOW}修复总结:${NC}"
echo "1. ✅ 所有配置文件已备份"
echo "2. ✅ 火山引擎超时设置为120秒"
echo "3. ✅ 阿里云超时设置为120秒（文本）/180秒（图像）"
echo "4. ✅ 重试次数设置为5次"
echo "5. ✅ 创建了增强版HTTP客户端配置"
echo "6. ✅ 创建了环境变量设置脚本"
echo "7. ✅ 创建了API连接测试脚本"

echo ""
echo -e "${BLUE}下一步操作:${NC}"
echo "1. 运行: source set_api_env.sh      # 设置环境变量"
echo "2. 运行: ./test_api_connection.sh   # 测试连接"
echo "3. 重启应用程序以应用新配置"
echo "4. 监控日志: tail -f logs/app.log   # 查看是否还有超时错误"

echo ""
echo -e "${YELLOW}如果问题仍然存在:${NC}"
echo "1. 检查网络连接和防火墙设置"
echo "2. 验证API密钥是否正确"
echo "3. 考虑配置HTTP代理"
echo "4. 联系网络管理员检查企业网络策略"
