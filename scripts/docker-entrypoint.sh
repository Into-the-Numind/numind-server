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
    
    # 检查 BGE 模型是否存在
    if [ -d "/app/model_cache/sentence_transformers/BAAI_bge-small-zh" ] || \
       [ -d "/app/model_cache/sentence_transformers/BAAI__bge-small-zh" ] || \
       [ -d "/app/model_cache/models--BAAI--bge-small-zh" ]; then
        log_info "✅ BGE 语义模型已就绪 (Found in /app/model_cache)"
    else
        log_warn "⚠️ BGE 模型未找到，准备自动下载..."
        should_download=true
    fi

    if [ "$should_download" = true ]; then
        log_info "🚀 开始下载模型... (这可能需要几分钟)"
        if python3 /app/scripts/download_models.py; then
            log_info "✅ 模型下载并缓存成功！"
        else
            log_error "❌ 模型下载失败"
            log_warn "⚠️ 系统将尝试继续启动，但在模型缺失的情况下部分功能可能不可用"
            return 0
        fi
    fi
    
    return 0
}

# 检查 Python 依赖
check_python_deps() {
    log_info "🔍 检查 Python 依赖..."
    
    if ! python3 -c "import sentence_transformers" 2>/dev/null; then
        log_warn "⚠️ sentence_transformers 未安装"
        return 1
    fi
    
    if ! python3 -c "import markitdown" 2>/dev/null; then
        log_warn "⚠️ markitdown 未安装"
        return 1
    fi
    
    log_info "✅ Python 依赖检查通过"
    return 0
}

# 检查并执行向量数据迁移（一次性，从 MySQL 迁移到 sqlitevec）
check_and_migrate_vectors() {
    local config_file="$1"

    # 检查迁移工具是否存在
    if [ ! -f "/app/migrate-vectors" ]; then
        log_debug "迁移工具不存在，跳过向量迁移"
        return 0
    fi

    # 使用哨兵文件避免重复迁移
    local env="${APP_ENV:-dev}"
    local sentinel="/opt/numind/${env}/.vectors_migrated"

    if [ -f "$sentinel" ]; then
        log_info "✅ 向量数据已迁移"
        return 0
    fi

    log_info "🔄 首次启动，执行向量数据迁移 (MySQL → sqlitevec)..."
    if CONFIG_FILE="$config_file" /app/migrate-vectors; then
        log_info "✅ 向量迁移完成"
        touch "$sentinel" 2>/dev/null || true
    else
        log_error "❌ 向量迁移失败，RAG 检索可能返回空结果"
        log_warn "   手动重试: docker exec <container> CONFIG_FILE=$config_file /app/migrate-vectors"
    fi

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

    # 向量数据迁移检查（一次性，非阻塞）
    check_and_migrate_vectors "$CONFIG_FILE" || true

    echo ""
    echo "╔════════════════════════════════════════════════════════╗"
    echo "║         🧠 启动语义切分服务...                         ║"
    echo "╚════════════════════════════════════════════════════════╝"
    echo ""

    # 启动 semantic-server (后台运行)
    # 确保日志目录存在
    mkdir -p /app/logs
    python3 /app/scripts/semantic_server.py > /app/logs/semantic_server.log 2>&1 &
    SEMANTIC_PID=$!

    log_info "✅ Semantic Server started (PID: $SEMANTIC_PID)"
    log_info "   Logs: /app/logs/semantic_server.log"

    # 设置清理钩子，确保容器退出时清理子进程
    cleanup() {
        log_info "🛑 Stopping background processes..."
        kill $SEMANTIC_PID 2>/dev/null || true
    }
    trap cleanup EXIT INT TERM

    # 主动轮询语义服务器健康状态（最多等待 120 秒）
    # BGE 模型加载通常需要 30-90 秒
    MAX_WAIT=120
    INTERVAL=5
    WAITED=0
    SEMANTIC_READY=false

    log_info "⏳ 等待语义服务就绪 (最多 ${MAX_WAIT}s)..."
    while [ $WAITED -lt $MAX_WAIT ]; do
        # 先检查进程是否还活着
        if ! kill -0 $SEMANTIC_PID 2>/dev/null; then
            log_error "❌ Semantic Server 进程已退出!"
            log_error "   最后 20 行日志:"
            tail -20 /app/logs/semantic_server.log 2>/dev/null || true
            break
        fi

        # 检查 health 端点
        if curl -sf http://localhost:9093/health > /tmp/semantic_health.json 2>/dev/null; then
            # 检查模型是否真正加载完成
            if grep -q '"model_ready":true' /tmp/semantic_health.json 2>/dev/null; then
                log_info "✅ 语义服务就绪! (耗时 ${WAITED}s)"
                SEMANTIC_READY=true
                break
            else
                log_info "⏳ 服务已启动，模型加载中... (${WAITED}s/${MAX_WAIT}s)"
            fi
        fi

        sleep $INTERVAL
        WAITED=$((WAITED + INTERVAL))
    done

    if [ "$SEMANTIC_READY" = false ]; then
        log_warn "⚠️ 语义服务未在 ${MAX_WAIT}s 内就绪，Go 应用将先启动"
        log_warn "   HybridSplitter 会在后续请求中自动重连"
        log_warn "   日志: /app/logs/semantic_server.log"
        # 打印最后几行日志帮助诊断
        log_warn "   最后 10 行日志:"
        tail -10 /app/logs/semantic_server.log 2>/dev/null || true
    fi
    
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
