#!/bin/bash

# 本地调试启动脚本 - Admin 服务
# 加载 config_local.yaml 配置文件

echo "==== 正在启动 numind-server (Admin) ===="
echo "配置文件: config_local.yaml"

# 启动 Admin 服务 (端口 9099)
go run cmd/numind-admin/main.go --config config_local.yaml
