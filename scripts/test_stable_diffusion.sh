#!/bin/bash

# 测试stable-diffusion集成
echo "测试stable-diffusion集成..."

# 设置环境变量
export CONFIG_FILE="config_local.yaml"

# 运行测试
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server

echo "编译并运行stable-diffusion测试..."
go run internal/numind/biz/ali/cmd/main.go

echo "测试完成！"
