#!/bin/bash

# 压力测试运行脚本

echo "========================================"
echo "  创建Book API 压力测试工具"
echo "========================================"
echo ""

# 获取脚本所在目录
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# 检查是否需要编译
if [ ! -f "stress_test_book" ] || [ "stress_test_book.go" -nt "stress_test_book" ]; then
    echo "正在编译..."
    go build -o stress_test_book stress_test_book.go
    if [ $? -ne 0 ]; then
        echo "❌ 编译失败"
        exit 1
    fi
    echo "✓ 编译成功"
    echo ""
fi

# 运行测试
echo "开始执行测试..."
echo ""
./stress_test_book

# 保存退出码
EXIT_CODE=$?

echo ""
echo "测试完成！"
exit $EXIT_CODE

