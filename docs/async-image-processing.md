# 异步图片处理功能

## 概述

本功能将图片批量上传处理改为异步执行模式，提高用户体验和系统性能。用户上传图片后立即获得任务ID，系统在后台处理图片并通过MQTT发送处理结果。

## 功能特性

- **异步处理**: 图片上传后立即返回任务ID，处理在后台进行
- **实时状态更新**: 通过MQTT实时推送处理状态和结果
- **并发控制**: 限制OCR并发数量，避免API限流
- **错误处理**: 完善的错误处理和状态反馈
- **结果通知**: 处理完成后自动创建书籍记录

## 架构设计

### 组件结构

```
ImageController (HTTP API)
    ↓
AsyncImageProcessor (异步处理器)
    ↓
┌─────────────────┬─────────────────┬─────────────────┐
│   Baidu OCR     │   Ali Qianwen   │   Ali Wanxiang  │
│   (文字识别)     │   (文本处理)     │   (图片生成)     │
└─────────────────┴─────────────────┴─────────────────┘
    ↓
MQTT Broker (消息队列)
    ↓
Client (客户端监听)
```

### 处理流程

1. **文件上传**: 用户通过HTTP API上传图片文件
2. **文件验证**: 验证文件格式、大小等
3. **任务创建**: 生成唯一任务ID，立即返回给用户
4. **异步处理**: 在后台goroutine中处理图片
5. **OCR识别**: 使用百度OCR识别图片中的文字
6. **文本处理**: 使用阿里千问处理识别出的文字
7. **图片生成**: 使用阿里万象生成综合图片
8. **结果保存**: 保存处理结果到数据库
9. **MQTT通知**: 通过MQTT发送处理结果

## 配置说明

### MQTT配置

在配置文件中添加MQTT配置：

```yaml
mqtt:
  broker: "localhost"      # MQTT broker地址
  port: 1883              # MQTT端口
  client_id: "numind-server"  # 客户端ID
  username: ""            # 用户名（可选）
  password: ""            # 密码（可选）
```

### 文件限制

- **文件大小**: 最大10MB
- **支持格式**: jpg, jpeg, png, gif, bmp, webp
- **并发限制**: 最多2个OCR请求并发

## API接口

### 批量上传图片

**请求**:
```
POST /v1/images/batch
Content-Type: multipart/form-data
Authorization: Bearer <token>

files: [图片文件1, 图片文件2, ...]
```

**响应**:
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

## MQTT消息格式

### 状态消息

**主题**: `numind/image/processing/status/{user_id}`

**消息格式**:
```json
{
  "task_id": "550e8400-e29b-41d4-a716-446655440000",
  "user_id": 123,
  "status": "processing|completed|failed",
  "message": "处理状态描述",
  "timestamp": 1640995200
}
```

### 结果消息

**主题**: `numind/image/processing/result/{user_id}`

**消息格式**:
```json
{
  "task_id": "550e8400-e29b-41d4-a716-446655440000",
  "user_id": 123,
  "status": "completed",
  "processed_images": [
    {
      "filename": "1640995200_image1.jpg",
      "url": "/static/1640995200_image1.jpg",
      "original_text": "识别的原始文字",
      "qianwen_result": "千问处理结果"
    }
  ],
  "final_result": {
    "wanxiang_result": "生成的图片URL",
    "book_id": 456,
    "total_texts": 3
  },
  "processing_time": "30s",
  "created_at": "2024-01-01T12:00:00Z"
}
```

## 客户端监听

### 使用提供的MQTT客户端

```bash
# 编译MQTT客户端
go build -o mqtt-client cmd/mqtt-client/main.go

# 运行客户端监听
./mqtt-client
```

### 自定义客户端

```go
package main

import (
    "github.com/eclipse/paho.mqtt.golang"
)

func main() {
    opts := mqtt.NewClientOptions()
    opts.AddBroker("tcp://localhost:1883")
    opts.SetClientID("my-client")
    
    client := mqtt.NewClient(opts)
    if token := client.Connect(); token.Wait() && token.Error() != nil {
        panic(token.Error())
    }
    
    // 订阅状态主题
    client.Subscribe("numind/image/processing/status/#", 0, func(client mqtt.Client, msg mqtt.Message) {
        fmt.Printf("Status: %s\n", string(msg.Payload()))
    })
    
    // 订阅结果主题
    client.Subscribe("numind/image/processing/result/#", 0, func(client mqtt.Client, msg mqtt.Message) {
        fmt.Printf("Result: %s\n", string(msg.Payload()))
    })
    
    // 保持连接
    select {}
}
```

## 部署说明

### 1. 安装MQTT Broker

推荐使用Mosquitto：

```bash
# Ubuntu/Debian
sudo apt-get install mosquitto mosquitto-clients

# macOS
brew install mosquitto

# 启动服务
mosquitto
```

### 2. 配置MQTT Broker

编辑 `/etc/mosquitto/mosquitto.conf`：

```conf
# 允许匿名连接
allow_anonymous true

# 监听端口
port 1883

# 日志级别
log_type all
```

### 3. 启动服务

```bash
# 启动MQTT broker
mosquitto

# 启动Numind服务器
go run cmd/numind/main.go -c config_local.yaml
```

## 错误处理

### 常见错误

1. **MQTT连接失败**
   - 检查MQTT broker是否运行
   - 检查网络连接和端口配置

2. **图片处理失败**
   - 检查文件格式和大小
   - 检查API密钥配置

3. **OCR识别失败**
   - 检查百度API配置
   - 检查图片质量

### 调试方法

1. **查看日志**:
   ```bash
   tail -f numind.log
   ```

2. **MQTT调试**:
   ```bash
   mosquitto_sub -t "numind/image/processing/#" -v
   ```

3. **API测试**:
   ```bash
   curl -X POST http://localhost:9091/v1/images/batch \
     -H "Authorization: Bearer <token>" \
     -F "files=@image1.jpg" \
     -F "files=@image2.jpg"
   ```

## 性能优化

### 并发控制

- OCR并发限制为2个，避免API限流
- 使用goroutine池处理图片
- 异步保存数据库记录

### 资源管理

- 及时释放文件句柄
- 使用连接池管理MQTT连接
- 合理设置超时时间

### 监控指标

- 处理时间统计
- 成功率监控
- 错误率统计
- 队列长度监控

## 扩展功能

### 可能的扩展

1. **任务队列**: 使用Redis或RabbitMQ替代MQTT
2. **进度跟踪**: 添加处理进度百分比
3. **重试机制**: 失败任务自动重试
4. **批量操作**: 支持批量任务管理
5. **结果缓存**: 缓存处理结果避免重复处理

### 集成建议

1. **WebSocket**: 实时推送处理状态到前端
2. **邮件通知**: 处理完成后发送邮件通知
3. **短信通知**: 重要任务完成后发送短信
4. **回调URL**: 支持HTTP回调通知 