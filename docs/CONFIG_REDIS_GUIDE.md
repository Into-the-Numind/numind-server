# 配置Redis秒级更新功能使用指南

## 概述

本系统实现了基于Redis的配置秒级更新功能，支持：
- 配置硬编码定义（防止配置错误）
- Redis缓存加速配置读取
- Pub/Sub实时通知配置变更
- 多容器实例自动同步

## 架构设计

### 配置流程

```
后台管理系统修改配置
    ↓
保存到数据库 (SystemConfigM)
    ↓
更新 Redis 缓存
    ↓
发布 Redis Pub/Sub 消息
    ↓
所有容器实例接收通知
    ↓
刷新本地缓存
    ↓
下次创建笔记时读取最新配置
```

### 核心组件

1. **配置定义** (`internal/numind/biz/config/config_definitions.go`)
   - 硬编码所有需要管理的配置项
   - 包含Key、Title、Description和默认值

2. **配置缓存** (`internal/numind/biz/config/config_cache.go`)
   - Redis缓存层
   - Pub/Sub通知机制

3. **配置读取器** (`internal/numind/biz/config/config_reader.go`)
   - 统一配置读取接口
   - 优先从Redis读取，未命中查数据库

4. **Redis客户端** (`internal/pkg/redis/redis.go`)
   - Redis连接管理
   - Pub/Sub支持

## 配置项列表

当前管理的配置项（硬编码在代码中）：

| 配置键 | 标题 | 说明 |
|--------|------|------|
| `ai_prompts.text_processing` | AI文本处理提示词 | AI处理笔记时的提示词 |
| `volc.model` | 火山引擎模型 | 火山引擎文本生成模型名称 |
| `volc.temperature` | 火山引擎温度参数 | 控制生成文本的随机性（0-1） |
| `volc.tokens` | 火山引擎Token数量 | 最大Token数量限制 |
| `ali.text.model` | 阿里云文本模型 | 阿里云文本生成模型名称 |

## 使用方式

### 1. 启动时自动同步

应用启动时会自动：
- 初始化Redis连接
- 同步配置到数据库（添加缺失项，更新标题和描述）
- 启动配置变更监听器

### 2. 后台管理系统更新配置

通过后台管理系统API更新配置：

```bash
PUT /v1/admin/system-configs/{key}
Content-Type: application/json
Authorization: Bearer {token}

{
  "value": "新的配置值",
  "description": "配置描述（可选）"
}
```

更新后会自动：
- 更新数据库
- 更新Redis缓存
- 发布Pub/Sub通知

### 3. 小程序端读取配置

创建笔记时，系统会自动：
- 优先从Redis读取配置
- Redis未命中时查数据库并缓存
- 使用最新配置处理笔记

## Redis配置

在 `config.yaml` 中配置Redis：

```yaml
redis:
  host: "localhost"      # Redis主机地址
  port: 6379             # Redis端口
  password: ""           # Redis密码（可选）
  db: 0                  # Redis数据库编号
  pool_size: 10          # 连接池大小
  min_idle_conns: 5      # 最小空闲连接数
```

## 测试

使用提供的测试脚本：

```bash
# 设置环境变量
export BASE_URL=http://localhost:9091
export ADMIN_TOKEN=your_admin_token

# 运行测试
./test_config_redis.sh
```

## 监控和调试

### 检查Redis缓存

```bash
# 查看配置缓存
redis-cli GET config:ai_prompts.text_processing
redis-cli GET config:volc.model

# 查看所有配置缓存
redis-cli KEYS config:*
```

### 监听配置变更通知

```bash
# 订阅配置变更频道
redis-cli SUBSCRIBE config:change
```

### 查看日志

配置相关的日志会包含以下关键词：
- `Config cache hit` - 缓存命中
- `Config cache miss` - 缓存未命中
- `Config updated and notification sent` - 配置已更新并发送通知
- `Refreshing config cache due to change notification` - 收到变更通知，刷新缓存

## 注意事项

1. **Redis可用性**
   - Redis不可用时，系统会降级到直接查数据库
   - 不会影响应用启动和基本功能

2. **配置同步**
   - 启动时自动同步配置定义
   - 只更新标题和描述，不覆盖用户修改的值

3. **多实例部署**
   - 所有容器实例共享同一个Redis
   - 配置变更会通过Pub/Sub通知所有实例

4. **缓存过期**
   - 配置缓存过期时间：1小时
   - 配置更新时会立即刷新缓存

## 添加新配置项

要添加新的配置项，需要：

1. 在 `config_definitions.go` 中添加配置定义：

```go
{
    Key:         "new.config.key",
    Title:       "新配置标题",
    DefaultValue: getDefaultValue("new.config.key", "default_value"),
    Description: "配置详细描述",
}
```

2. 在 `config_reader.go` 中添加读取方法（如需要）

3. 重启应用，配置会自动同步到数据库

## 故障排查

### Redis连接失败
- 检查Redis服务是否运行
- 检查配置文件中的Redis地址和端口
- 查看日志中的Redis连接错误

### 配置更新不生效
- 检查Redis缓存是否更新
- 检查Pub/Sub通知是否发送
- 查看应用日志中的配置读取记录

### 多实例配置不一致
- 确保所有实例连接到同一个Redis
- 检查Pub/Sub订阅是否正常
- 查看各实例的日志确认是否收到通知

