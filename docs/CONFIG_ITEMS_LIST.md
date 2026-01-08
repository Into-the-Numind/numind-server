# 配置项列表

## 概述

系统管理的所有配置项都硬编码在 `internal/numind/biz/config/config_definitions.go` 中，启动时自动同步到 `system_config` 表。

## 配置项统计

**总计：17个配置项**

## 配置项详情

### AI提示词配置 (1项)

| 配置键 | 标题 | 默认值来源 |
|--------|------|-----------|
| `ai_prompts.text_processing` | AI文本处理提示词 | yaml配置文件或空字符串 |

### 火山引擎配置 (7项)

| 配置键 | 标题 | 默认值来源 |
|--------|------|-----------|
| `volc.api_key` | 火山引擎API密钥 | yaml配置文件或空字符串 |
| `volc.base_url` | 火山引擎API地址 | yaml配置文件或 "https://ark.cn-beijing.volces.com/api/v3" |
| `volc.model` | 火山引擎模型 | yaml配置文件或 "doubao-seed-1-6-flash-250828" |
| `volc.temperature` | 火山引擎温度参数 | yaml配置文件或 "0.5" |
| `volc.tokens` | 火山引擎Token数量 | yaml配置文件或 "2000" |
| `volc.timeout` | 火山引擎超时时间 | yaml配置文件或 "180s" |
| `volc.max_retries` | 火山引擎最大重试次数 | yaml配置文件或 "3" |

### 阿里云配置 (9项)

#### 通用配置 (2项)

| 配置键 | 标题 | 默认值来源 |
|--------|------|-----------|
| `ali.api_key` | 阿里云API密钥 | yaml配置文件或空字符串 |
| `ali.api_url` | 阿里云API地址 | yaml配置文件或 "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis" |

#### 文本生成服务 (2项)

| 配置键 | 标题 | 默认值来源 |
|--------|------|-----------|
| `ali.text.model` | 阿里云文本模型 | yaml配置文件或 "qwen-turbo" |
| `ali.text.timeout` | 阿里云文本生成超时时间 | yaml配置文件或 "180s" |

#### 图像生成服务 (3项)

| 配置键 | 标题 | 默认值来源 |
|--------|------|-----------|
| `ali.image.api_key` | 阿里云图像服务API密钥 | yaml配置文件或空字符串 |
| `ali.image.model` | 阿里云图像生成模型 | yaml配置文件或 "wanx2.1-t2i-turbo" |
| `ali.image.timeout` | 阿里云图像生成超时时间 | yaml配置文件或 "180s" |

#### Stable Diffusion服务 (2项)

| 配置键 | 标题 | 默认值来源 |
|--------|------|-----------|
| `ali.stable_diffusion.model` | 阿里云Stable Diffusion模型 | yaml配置文件或 "stable-diffusion-3.5-large-turbo" |
| `ali.stable_diffusion.timeout` | 阿里云Stable Diffusion超时时间 | yaml配置文件或 "300s" |

## Redis环境分离配置

不同环境使用不同的Redis数据库编号，确保环境隔离：

| 环境 | Redis DB | 配置文件 |
|------|----------|----------|
| local | 0 | `config_local.yaml` |
| dev | 1 | `config_dev.yaml` |
| qa | 2 | `config_qa.yaml` |
| prod | 3 | `config_prod.yaml` |

### Redis配置示例

```yaml
# Redis 缓存相关配置
redis:
  host: localhost      # Redis 机器 IP
  port: 6379           # Redis 端口
  password: ""         # Redis 密码
  db: 1                # Redis 数据库编号（根据环境不同：0/1/2/3）
  pool_size: 10        # Redis 连接池大小
  min_idle_conns: 5    # Redis 最小空闲连接数
```

## 配置同步规则

1. **启动时自动同步**：应用启动时，`InitDefaultConfigs()` 会自动：
   - 从硬编码定义获取所有配置项
   - 对比数据库，添加缺失的配置项
   - 更新标题和描述（不覆盖用户修改的值）
   - 清理旧配置项

2. **初始值来源**：
   - 优先从yaml配置文件读取
   - 如果yaml中不存在，使用代码中的fallback值

3. **用户值保护**：
   - 启动时同步不会覆盖用户已修改的配置值
   - 只更新标题和描述

## 使用方式

### 后台管理系统更新配置

通过API更新配置，会自动：
- 更新数据库
- 更新Redis缓存
- 发布Pub/Sub通知所有实例

### 小程序端读取配置

创建笔记时，系统自动从Redis/数据库读取最新配置。

## 注意事项

1. **配置键格式**：使用点号分隔，如 `volc.model`、`ali.text.model`
2. **环境隔离**：不同环境使用不同的Redis数据库，确保配置不会互相影响
3. **配置安全**：敏感配置（如API密钥）建议通过后台管理系统设置，不要硬编码在代码中

