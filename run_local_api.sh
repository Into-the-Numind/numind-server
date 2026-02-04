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

# 配置 WeCom Agent 环境变量
export WECOM_CORP_ID="wwb71317627b6b70d8"
export WECOM_SECRET="7-55a-RDZgDzC5jhH4YjxF6zwtFRO0Mwj5_6TxQGUtc"
export WECOM_POLLER_ENABLED="true"
# 使用 config_local.yaml 中的远程数据库配置
export MYSQL_DSN="root:Numind2025@tcp(49.233.219.254:13306)/numind-dev?charset=utf8mb4&parseTime=True&loc=Local"


echo "==== 正在启动 wecom-agent (消息轮询) ===="
go run cmd/wecom-agent/main.go &
WECOM_PID=$!

echo "==== 正在启动 semantic-server (语义切分服务) ===="
python3 scripts/semantic_server.py > semantic_server.log 2>&1 &
SEMANTIC_PID=$!

# 等待 Python 服务启动 (模型加载可能需要几秒)
echo "正在等待语义模型加载..."
sleep 5

cleanup() {
    echo "Stopping background processes..."
    kill $WECOM_PID 2>/dev/null
    kill $SEMANTIC_PID 2>/dev/null
}
trap cleanup EXIT INT TERM

# 启动 API 服务 (端口 9091)
echo "==== 正在启动 numind-server (API) ===="
go run cmd/numind/main.go --config config_local.yaml
