#!/bin/bash
# 检查语义切分器部署状态

set -e

echo "==============================================="
echo "Semantic Splitter Deployment Check"
echo "==============================================="

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

CHECK_PASS=0
CHECK_FAIL=0

check_pass() {
    echo -e "${GREEN}✅${NC} $1"
    ((CHECK_PASS++))
}

check_fail() {
    echo -e "${RED}❌${NC} $1"
    ((CHECK_FAIL++))
}

check_warn() {
    echo -e "${YELLOW}⚠️${NC} $1"
}

# 1. 检查 Python3
echo ""
echo "1. Checking Python3..."
if command -v python3 &> /dev/null; then
    PYTHON_VERSION=$(python3 --version 2>&1)
    check_pass "Python3 found: $PYTHON_VERSION"
else
    check_fail "Python3 not found"
    echo "   Install: sudo apt-get install python3 (Ubuntu/Debian)"
    echo "            brew install python3 (macOS)"
fi

# 2. 检查 sentence-transformers
echo ""
echo "2. Checking sentence-transformers..."
if python3 -c "from sentence_transformers import SentenceTransformer" 2>/dev/null; then
    check_pass "sentence-transformers installed"
else
    check_fail "sentence-transformers not installed"
    echo "   Install: pip3 install sentence-transformers"
fi

# 3. 检查 numpy
echo ""
echo "3. Checking numpy..."
if python3 -c "import numpy" 2>/dev/null; then
    check_pass "numpy installed"
else
    check_fail "numpy not installed"
    echo "   Install: pip3 install numpy"
fi

# 4. 检查脚本文件
echo ""
echo "4. Checking script files..."
SCRIPT_PATHS=(
    "scripts/semantic_splitter.py"
    "/app/scripts/semantic_splitter.py"
)

SCRIPT_FOUND=false
for path in "${SCRIPT_PATHS[@]}"; do
    if [ -f "$path" ]; then
        check_pass "Script found at: $path"
        SCRIPT_FOUND=true
        break
    fi
done

if [ "$SCRIPT_FOUND" = false ]; then
    check_fail "semantic_splitter.py not found"
fi

# 5. 测试切分功能
echo ""
echo "5. Testing semantic split..."
TEST_TEXT="这是一个测试。我们的产品非常好。价格是100元。欢迎购买。"

# 创建临时测试文件
TEST_FILE=$(mktemp)
echo "$TEST_TEXT" > "$TEST_FILE"

SCRIPT_PATH=""
for path in "${SCRIPT_PATHS[@]}"; do
    if [ -f "$path" ]; then
        SCRIPT_PATH="$path"
        break
    fi
done

if [ -n "$SCRIPT_PATH" ]; then
    if python3 "$SCRIPT_PATH" "$TEST_FILE" 0.5 10 1000 > /dev/null 2>&1; then
        check_pass "Semantic split test passed"
    else
        check_fail "Semantic split test failed"
        echo "   This may be due to model downloading on first run"
    fi
else
    check_warn "Skipping split test (script not found)"
fi

rm -f "$TEST_FILE"

# 6. 检查 Go 编译
echo ""
echo "6. Checking Go build..."
cd "$(dirname "$0")/.." || exit 1
if go build ./internal/numind/biz/salesrag/service/... 2>/dev/null; then
    check_pass "Go code compiles successfully"
else
    check_fail "Go compilation failed"
fi

# 总结
echo ""
echo "==============================================="
echo "Summary"
echo "==============================================="
echo -e "Passed: ${GREEN}$CHECK_PASS${NC}"
echo -e "Failed: ${RED}$CHECK_FAIL${NC}"

if [ $CHECK_FAIL -eq 0 ]; then
    echo ""
    echo -e "${GREEN}✅ Semantic splitter is ready!${NC}"
    exit 0
else
    echo ""
    echo -e "${YELLOW}⚠️  Some checks failed. Run the following to fix:${NC}"
    echo "   bash scripts/install_semantic_deps.sh"
    exit 1
fi
