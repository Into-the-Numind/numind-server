#!/bin/bash

# 本地调试启动脚本 - API 服务
# 加载 config_local.yaml 配置文件

echo "==== 正在启动 numind-server (API) ===="
echo "配置文件: config_local.yaml"

# 创建本地数据目录（如果需要）
mkdir -p ./data/image

# 增加数据库连接超时相关环境变量
# Viper 会将这些映射到 db.max-connection-life-time 等配置项
export NUMIND_DB_MAX_CONNECTION_LIFE_TIME="30s"

# Ensure we use the python environment with installed packages (miniconda)
export PATH="/opt/homebrew/Caskroom/miniconda/base/bin:$PATH"

# 启动 API 服务 (端口 9091)
go run cmd/numind/main.go --config config_local.yaml
