# HTTP客户端优化方案

## 概述

针对现有的HTTP流式处理问题，我们实现了一套完整的HTTP客户端优化方案，包含超时控制、重试机制、断点续传和流式处理优化。

## 主要问题分析

### 1. JSON解析错误
- **问题**: "JSON validation failed: unexpected end of JSON input"
- **原因**: HTTP响应被截断，导致JSON不完整
- **影响**: API调用失败，用户体验差

### 2. 网络超时问题
- **问题**: 网络请求超时设置不一致
- **原因**: ResponseHeaderTimeout与整体超时时间不匹配
- **影响**: 实际超时时间比预期短

### 3. 缺乏重试机制
- **问题**: 网络错误时没有自动重试
- **原因**: 使用标准HTTP客户端，缺乏重试逻辑
- **影响**: 网络波动时API调用失败率高

## 优化方案

### 1. 优化的HTTP客户端 (`internal/pkg/httpclient/client.go`)

#### 核心特性
- **统一超时控制**: 连接超时、响应头超时、整体超时协调一致
- **智能重试机制**: 指数退避重试，支持自定义重试策略
- **连接池优化**: 最大空闲连接数、空闲连接超时等配置

#### 配置示例
```go
config := &httpclient.Config{
    Timeout:               120 * time.Second,
    ConnectTimeout:        30 * time.Second,
    ResponseHeaderTimeout: 120 * time.Second,
    MaxRetries:           3,
    RetryDelay:           1 * time.Second,
    RetryBackoff:         2.0,
}
```

### 2. 断点续传下载 (`internal/pkg/httpclient/resume.go`)

#### 功能特性
- **自动断点续传**: 检测已下载文件，自动设置Range header
- **进度监控**: 实时下载进度和速度显示
- **错误恢复**: 下载失败时自动重试

### 3. 流式处理优化 (`internal/pkg/httpclient/streaming.go`)

#### 支持格式
- **SSE (Server-Sent Events)**: 标准SSE格式解析
- **JSON流**: 逐行JSON处理
- **超时控制**: 流式处理超时保护

## 集成到现有代码

### 1. Volc API优化 (`internal/numind/biz/volc/volc.go`)

#### 主要改进
- 使用优化的HTTP客户端替换标准客户端
- 添加响应完整性检查
- 实现JSON响应清理和修复
- 增强错误日志和诊断信息

### 2. 阿里云API优化 (`internal/numind/biz/ali/ali.go`)

#### 主要改进
- 统一使用优化的HTTP客户端
- 配置驱动的超时和重试设置
- 更好的错误处理和日志记录

## 配置管理

### 1. 环境配置文件
所有超时和重试配置都放在配置文件中，支持不同环境：

```yaml
# config_prod.yaml
volc:
  timeout: 120s
  max_retries: 3

ali:
  text:
    timeout: 120s
  image:
    timeout: 180s
```

## 错误处理和诊断

### 1. 响应完整性检查
- 检查响应体长度
- 验证JSON结构完整性
- 检测截断的响应

### 2. JSON修复机制
- 自动清理不完整的JSON
- 尝试修复截断的响应
- 详细的错误日志记录

## 总结

通过这套HTTP客户端优化方案，我们解决了以下关键问题：

1. **响应截断问题**: 通过响应完整性检查和JSON修复机制
2. **超时不一致**: 统一超时配置，确保超时行为一致
3. **缺乏重试**: 智能重试机制，提高API调用成功率
4. **流式处理**: 优化的流式处理，支持多种格式
5. **断点续传**: 支持大文件下载的断点续传

这些优化显著提高了系统的稳定性和用户体验，特别是在网络环境不稳定的情况下。
