#!/bin/bash

echo "Testing Volc Logging..."

# 编译并运行volc测试
cd internal/numind/biz/volc/cmd
go build -o volc-test main.go

if [ -f "./volc-test" ]; then
    echo "Running volc test with logging..."
    # 设置环境变量
    export VOLC_API_KEY="e709f501-2307-4e0c-9115-b6a3065ac06b"
    export VOLC_BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
    export VOLC_MODEL="doubao-seed-1-6-250715"
    
    # 运行测试
    ./volc-test
    rm ./volc-test
else
    echo "Failed to build volc test"
    exit 1
fi

echo "Volc logging test completed!"
