#!/bin/bash
set -e

# ============================================
# Numind Server Docker 入口脚本
# 功能：
# 1. 检查并下载语义切分模型
# 2. 选择正确的配置文件
# 3. 启动应用
# ============================================

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_debug() {
    echo -e "${BLUE}[DEBUG]${NC} $1"
}

# 检查并下载语义切分模型
check_and_download_model() {
    log_info "🔍 检查语义切分模型..."
    
    # 设置环境变量
    export SENTENCE_TRANSFORMERS_HOME=/app/model_cache
    export HF_HOME=/app/model_cache
    
    # 中国大陆用户使用镜像源
    if [ -z "$HF_ENDPOINT" ]; then
        export HF_ENDPOINT=https://hf-mirror.com
        log_info "🌐 使用镜像源: $HF_ENDPOINT"
    fi
    
    # 检查模型是否已存在
    if python3 << 'EOF' 2>/dev/null; then
import os
import sys

model_dir = '/app/model_cache/sentence_transformers/BAAI__bge-small-zh'
if os.path.exists(model_dir):
    # 检查关键文件是否存在
    required_files = ['config.json', 'pytorch_model.bin', 'tokenizer.json']
    for f in required_files:
        file_path = os.path.join(model_dir, f)
        if not os.path.exists(file_path) and not os.path.exists(file_path + '.safetensors'):
            print(f'Missing: {f}')
            sys.exit(1)
    print('Model exists and is complete')
    sys.exit(0)
else:
    print('Model directory not found')
    sys.exit(1)
EOF
    then
        log_info "✅ 语义切分模型已就绪"
        return 0
    fi
    
    log_warn "⚠️ 模型未找到或文件不完整，尝试下载..."
    
    # 尝试下载模型（带重试）
    max_retries=3
    for i in $(seq 1 $max_retries); do
        log_info "🔄 第 $i 次尝试下载模型..."
        
        if python3 << 'EOF' 2>&1; then
import os
import sys
import time

os.environ['SENTENCE_TRANSFORMERS_HOME'] = '/app/model_cache'
os.environ['HF_HOME'] = '/app/model_cache'

try:
    from sentence_transformers import SentenceTransformer
    # 使用离线模式检查
    os.environ['TRANSFORMERS_OFFLINE'] = '0'
    model = SentenceTransformer('BAAI/bge-small-zh', cache_folder='/app/model_cache')
    print('✅ Model downloaded successfully')
    sys.exit(0)
except Exception as e:
    print(f'Error: {e}')
    sys.exit(1)
EOF
        then
            log_info "✅ 模型下载成功"
            return 0
        else
            log_error "❌ 第 $i 次下载失败"
            if [ $i -lt $max_retries ]; then
                sleep_time=$((5 * i))
                log_info "⏳ 等待 ${sleep_time}秒后重试..."
                sleep $sleep_time
            fi
        fi
    done
    
    log_warn "⚠️ 模型下载失败，系统将使用规则切分作为回退"
    log_warn "📋 手动下载命令:"
    log_warn "   docker exec <container> python3 -c \"from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-small-zh')\""
    
    return 0  # 不阻止启动
}

# 检查 Python 依赖
check_python_deps() {
    log_info "🔍 检查 Python 依赖..."
    
    if ! python3 -c "import sentence_transformers" 2>/dev/null; then
        log_warn "⚠️ sentence_transformers 未安装"
        return 1
    fi
    
    if ! python3 -c "import numpy" 2>/dev/null; then
        log_warn "⚠️ numpy 未安装"
        return 1
    fi
    
    log_info "✅ Python 依赖检查通过"
    return 0
}

# 显示系统信息
show_system_info() {
    log_info "📊 系统信息:"
    log_info "   环境: ${APP_ENV:-dev}"
    log_info "   时区: $(cat /etc/timezone 2>/dev/null || echo 'Asia/Shanghai')"
    log_info "   Python: $(python3 --version 2>&1 || echo 'N/A')"
    log_info "   模型缓存: /app/model_cache"
    
    # 检查模型缓存大小
    if [ -d "/app/model_cache" ]; then
        cache_size=$(du -sh /app/model_cache 2>/dev/null | cut -f1)
        log_info "   缓存大小: $cache_size"
    fi
}

# 主启动流程
main() {
    echo ""
    echo "╔════════════════════════════════════════════════════════╗"
    echo "║         🚀 Numind Server 正在启动                      ║"
    echo "╚════════════════════════════════════════════════════════╝"
    echo ""
    
    # 显示系统信息
    show_system_info
    
    echo ""
    
    # 检查 Python 依赖
    if check_python_deps; then
        # 检查模型（非阻塞）
        check_and_download_model || true
    else
        log_warn "⚠️ Python 依赖检查失败，跳过模型下载"
    fi
    
    echo ""
    
    # 根据环境变量选择配置文件
    if [ -n "$APP_ENV" ]; then
        CONFIG_FILE="/app/config_${APP_ENV}.yaml"
    else
        CONFIG_FILE="/app/config_dev.yaml"
    fi
    
    # 检查配置文件是否存在
    if [ ! -f "$CONFIG_FILE" ]; then
        log_error "配置文件不存在: $CONFIG_FILE"
        log_info "可用的配置文件:"
        ls -la /app/config_*.yaml 2>/dev/null || true
        exit 1
    fi
    
    log_info "📄 使用配置文件: $CONFIG_FILE"
    
    echo ""
    echo "╔════════════════════════════════════════════════════════╗"
    echo "║         🟢 启动应用...                                 ║"
    echo "╚════════════════════════════════════════════════════════╝"
    echo ""
    
    # 启动应用
    exec /app/numind -c "$CONFIG_FILE" "$@"
}

# 运行主函数
main "$@"
