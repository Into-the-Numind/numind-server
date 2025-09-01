#!/bin/bash

echo "Testing Volc Text Generation..."

# 设置环境变量（如果需要）
export VOLC_API_KEY="your_volc_api_key_here"
export VOLC_BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
export VOLC_MODEL="deepseek-v3-250324"

# 编译并运行volc测试
cd internal/numind/biz/volc/cmd
go build -o volc-test main.go

if [ -f "./volc-test" ]; then
    echo "Running volc test..."
    ./volc-test
    rm ./volc-test
else
    echo "Failed to build volc test"
    exit 1
fi

echo "Volc test completed!"
