#!/bin/bash
# ============================================
# 切分策略快速检查脚本
# ============================================

set -e

CONTAINER_NAME=${1:-numind-server-dev}

echo "=========================================="
echo "      切分策略诊断工具"
echo "=========================================="
echo ""
echo "容器: $CONTAINER_NAME"
echo ""

# 检查容器状态
echo "[1/5] 检查容器状态..."
if ! docker ps -q -f name="$CONTAINER_NAME" | grep -q .; then
    echo "❌ 容器未运行"
    exit 1
fi
echo "✅ 容器运行正常"
echo ""

# 检查语义切分器可用性
echo "[2/5] 检查语义切分器..."
if docker exec "$CONTAINER_NAME" python3 -c "from sentence_transformers import SentenceTransformer" 2>/dev/null; then
    echo "✅ 语义切分器可用"
else
    echo "❌ 语义切分器不可用"
    echo "   原因: Python 包未安装或模型未下载"
fi
echo ""

# 检查模型文件
echo "[3/5] 检查模型文件..."
if docker exec "$CONTAINER_NAME" test -d /app/model_cache/sentence_transformers/BAAI__bge-small-zh 2>/dev/null; then
    SIZE=$(docker exec "$CONTAINER_NAME" du -sh /app/model_cache/sentence_transformers/BAAI__bge-small-zh 2>/dev/null | cut -f1)
    echo "✅ 模型文件存在 ($SIZE)"
else
    echo "⚠️  模型文件不存在"
fi
echo ""

# 查看最近的切分相关日志
echo "[4/5] 查看切分日志..."
echo ""
docker logs --tail 200 "$CONTAINER_NAME" 2>&1 | grep -E "(Splitter|chunk|Chunk)" | tail -20 || echo "暂无切分日志"
echo ""

# 提供建议
echo "[5/5] 诊断建议"
echo ""

# 检查是否启用了调试模式
if docker exec "$CONTAINER_NAME" printenv SPLITTER_DEBUG 2>/dev/null | grep -q "1"; then
    echo "✅ 调试模式已启用"
else
    echo "💡 建议：启用调试模式以查看详细切分信息"
    echo "   方法：添加环境变量 SPLITTER_DEBUG=1"
    echo ""
    echo "   示例："
    echo "   docker stop $CONTAINER_NAME"
    echo "   docker run -d --name $CONTAINER_NAME -e SPLITTER_DEBUG=1 ..."
fi

echo ""
echo "=========================================="
echo "诊断完成！"
echo "=========================================="
echo ""
echo "如需查看实时切分日志，请运行："
echo "  docker logs -f $CONTAINER_NAME | grep -E '(Splitter|Chunk)'"
echo ""
