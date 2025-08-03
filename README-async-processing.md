# 异步图片处理功能 - 快速使用指南

## 问题解决

你遇到的错误 `"MQTT client not connected"` 是因为MQTT客户端没有连接到MQTT broker。

## 解决步骤

### 1. 安装MQTT Broker

**macOS:**
```bash
brew install mosquitto
```

**Ubuntu/Debian:**
```bash
sudo apt-get install mosquitto mosquitto-clients
```

### 2. 启动MQTT Broker

```bash
# 启动MQTT broker
mosquitto
```

### 3. 测试MQTT连接

```bash
# 运行测试脚本
./scripts/test-mqtt.sh
```

### 4. 启动服务器

```bash
# 启动Numind服务器
go run cmd/numind/main.go -c config_local.yaml
```

### 5. 监听MQTT消息

在另一个终端窗口运行：

```bash
# 监听所有图片处理相关的消息
mosquitto_sub -t "numind/image/processing/#' -v
```

或者使用我们提供的MQTT客户端：

```bash
# 编译MQTT客户端
go build -o mqtt-client cmd/mqtt-client/main.go

# 运行客户端
./mqtt-client
```

## 功能说明

### 异步处理流程

1. **上传图片** → 立即返回任务ID
2. **后台处理** → OCR识别 + 文本处理 + 图片生成
3. **MQTT通知** → 实时推送处理状态和结果

### API调用示例

```bash
curl -X POST http://localhost:9091/v1/images/batch \
  -H "Authorization: Bearer <your-token>" \
  -F "files=@image1.jpg" \
  -F "files=@image2.jpg"
```

**响应:**
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "task_id": "550e8400-e29b-41d4-a716-446655440000",
    "user_id": 123,
    "status": "processing",
    "message": "图片处理已开始，请通过MQTT监听处理结果",
    "mqtt_topic": "numind/image/processing/status/123",
    "result_topic": "numind/image/processing/result/123"
  }
}
```

### MQTT消息示例

**状态消息:**
```json
{
  "task_id": "550e8400-e29b-41d4-a716-446655440000",
  "user_id": 123,
  "status": "processing",
  "message": "开始处理图片",
  "timestamp": 1640995200
}
```

**结果消息:**
```json
{
  "task_id": "550e8400-e29b-41d4-a716-446655440000",
  "user_id": 123,
  "status": "completed",
  "processed_images": [...],
  "final_result": {
    "wanxiang_result": "生成的图片URL",
    "book_id": 456,
    "total_texts": 3
  },
  "processing_time": "30s",
  "created_at": "2024-01-01T12:00:00Z"
}
```

## 故障排除

### 常见问题

1. **MQTT连接失败**
   - 确保mosquitto正在运行
   - 检查端口1883是否被占用
   - 运行 `./scripts/test-mqtt.sh` 测试连接

2. **图片处理失败**
   - 检查文件格式和大小限制
   - 查看服务器日志获取详细错误信息

3. **API调用失败**
   - 确保服务器正在运行
   - 检查JWT token是否有效
   - 验证请求格式是否正确

### 调试命令

```bash
# 查看服务器日志
tail -f numind.log

# 测试MQTT连接
mosquitto_pub -h localhost -p 1883 -t "test" -m "hello"

# 监听MQTT消息
mosquitto_sub -t "numind/image/processing/#' -v

# 检查MQTT broker状态
ps aux | grep mosquitto
```

## 配置说明

在 `config_local.yaml` 中确保MQTT配置正确：

```yaml
mqtt:
  broker: "localhost"
  port: 1883
  client_id: "numind-server-local"
  username: ""
  password: ""
```

## 性能优化

- 图片处理是异步的，不会阻塞HTTP响应
- OCR并发限制为2个，避免API限流
- 处理结果通过MQTT实时推送
- 支持多文件批量处理 