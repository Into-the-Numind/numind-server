# Stable-Diffusion 3.5 Large Turbo 集成实现

## 概述

本文档描述了如何将阿里云的 stable-diffusion-3.5-large-turbo 模型集成到 numind-server 中，替换原有的万相图像生成逻辑。

## 集成的 API 端点

### 1. 图像合成 API
- **URL**: `POST https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis`
- **用途**: 提交异步图像生成任务

### 2. 任务查询 API
- **URL**: `GET https://dashscope.aliyuncs.com/api/v1/tasks/{task_id}`
- **用途**: 查询异步任务状态和获取结果

## 实现的功能

### 1. 新增业务方法
在 `internal/numind/biz/ali/ali.go` 中添加了 `StableDiffusionImageAsync` 方法：

```go
func (a *aliBiz) StableDiffusionImageAsync(prompt, size string) (string, error)
```

### 2. 接口更新
更新了以下接口以支持 stable-diffusion：
- `AliBiz` 接口
- `AsyncAliBiz` 接口
- 相关的适配器实现

### 3. 业务逻辑替换
在以下文件中将万相调用替换为 stable-diffusion：
- `internal/numind/biz/image/async_processor.go`
- `internal/numind/biz/book/async_processor.go`

## 配置更新

### 1. config_local.yaml
```yaml
ali:
  # ... 现有配置 ...
  stable_diffusion:
    api_key: "sk-4b081bdaaa14454ca19d1ed5d031cd10"
    api_url: "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"
    model: "stable-diffusion-3.5-large-turbo"
    timeout: 300s
```

### 2. config_dev.yaml
```yaml
ali:
  # ... 现有配置 ...
  stable_diffusion:
    api_key: "sk-4b081bdaaa14454ca19d1ed5d031cd10"
    api_url: "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"
    model: "stable-diffusion-3.5-large-turbo"
    timeout: 300s
```

## API 参数说明

### 请求参数
- **model**: `stable-diffusion-3.5-large-turbo`
- **input.prompt**: 图像生成提示词
- **parameters.size**: 图像尺寸 (默认: 1024*1024)
- **parameters.n**: 生成图像数量 (默认: 1)
- **parameters.steps**: 去噪推理步数 (默认: 40)
- **parameters.cfg**: 提示词遵循度 (默认: 4.5)
- **parameters.seed**: 随机种子 (默认: 42)
- **parameters.shift**: 偏移值 (默认: 3.0)

### 响应参数
- **output.task_id**: 异步任务ID
- **output.task_status**: 任务状态
- **output.results**: 生成的图像结果
- **usage.image_count**: 成功生成的图像数量

## 使用方式

### 1. 在图片处理中使用
```go
// 替换原有的万相调用
stableDiffusionResult, err := aliBiz.StableDiffusionImageAsync(prompt, "1024*1024")
```

### 2. 在书籍创建中使用
```go
// 替换原有的万相调用
remoteImageUrl, err := aliBiz.StableDiffusionImageAsync(imagePrompt, "1024*1024")
```

## 测试

### 1. 运行测试脚本
```bash
./scripts/test_stable_diffusion.sh
```

### 2. 直接测试
```bash
go run internal/numind/biz/ali/cmd/main.go
```

## 优势

### 1. 图像质量提升
- stable-diffusion-3.5-large-turbo 相比万相模型有更好的图像质量
- 支持更精细的参数控制

### 2. 稳定性增强
- 异步处理机制，避免超时问题
- 完善的错误处理和重试机制

### 3. 性能优化
- 支持批量生成
- 可配置的推理步数和质量参数

## 注意事项

### 1. API 限制
- 需要有效的阿里云 API Key
- 异步任务有最大轮询次数限制 (30次)
- 任务状态查询间隔为 3 秒

### 2. 超时设置
- 图像生成超时时间设置为 5 分钟
- 任务提交超时时间为 30 秒

### 3. 错误处理
- 任务失败时会返回详细错误信息
- 超时情况下会提示用户稍后重试

## 迁移指南

### 1. 代码更新
- 将 `WanxiangImageAsync` 调用替换为 `StableDiffusionImageAsync`
- 移除 `style` 参数（stable-diffusion 不支持）

### 2. 配置更新
- 在配置文件中添加 `stable_diffusion` 配置段
- 确保 API Key 有效且有足够权限

### 3. 测试验证
- 运行测试脚本验证集成是否成功
- 检查图像生成质量和性能表现

## 故障排除

### 1. 常见问题
- **API Key 无效**: 检查配置文件中的 API Key 是否正确
- **任务超时**: 增加 `maxTries` 值或调整 `interval` 时间
- **图像生成失败**: 检查提示词是否包含敏感内容

### 2. 日志分析
- 查看控制台输出的任务状态和进度信息
- 检查错误日志中的详细错误信息

### 3. 性能调优
- 根据需求调整 `steps` 和 `cfg` 参数
- 优化提示词以获得更好的生成效果
