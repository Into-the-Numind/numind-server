# 配置文件超时设置同步总结

## 概述

根据用户要求，已将qianwen、wanxiang、volc的超时时间配置同步到所有环境配置文件中，确保超时设置的一致性和可配置性。

## 同步的配置文件

### 1. `config_local.yaml` - 本地开发环境
### 2. `config_dev.yaml` - 开发环境
### 3. `config_qa.yaml` - 测试环境
### 4. `config_prod.yaml` - 生产环境

## 超时配置详情

### Volc API 配置

```yaml
volc:
  api_key: "xxx"
  base_url: "https://ark.cn-beijing.volces.com/api/v3"
  model: "xxx"
  temperature: 0.5
  tokens: 2000
  timeout: 120s  # 从30s改为120s
  max_retries: 3
```

**变更**: `timeout: 30s` → `timeout: 120s`

### 阿里云 API 配置

#### 千问文本生成 (Qianwen)

```yaml
ali:
  text:
    api_key: "sk-xxx"
    api_url: "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"
    model: "qwq-plus"
    timeout: 120s  # 新增：千问文本生成超时时间
```

#### 万象图像生成 (Wanxiang)

```yaml
ali:
  image:
    api_key: "sk-xxx"
    api_url: "https://dashscope.aliyuncs.com/api/v1/services/aigc/text2image/image-synthesis"
    model: "wanx2.1-t2i-turbo"
    timeout: 180s  # 新增：万象图像生成超时时间
```

## 超时时间设置说明

### 1. Volc API: 120秒
- **用途**: 文本生成和对话
- **原因**: 网络较慢时需要更长的超时时间
- **优化**: 从30秒增加到120秒

### 2. 千问文本生成: 120秒
- **用途**: 文本处理和结构化
- **原因**: 复杂文本处理需要更多时间
- **优化**: 新增配置，避免硬编码

### 3. 万象图像生成: 180秒
- **用途**: AI图像生成
- **原因**: 图像生成通常比文本生成耗时更长
- **优化**: 新增配置，给予足够的处理时间

## 配置同步状态

| 配置文件 | Volc Timeout | Qianwen Timeout | Wanxiang Timeout | 状态 |
|---------|-------------|----------------|------------------|------|
| config_local.yaml | ✅ 120s | ✅ 120s | ✅ 180s | 已同步 |
| config_dev.yaml | ✅ 120s | ✅ 120s | ✅ 180s | 已同步 |
| config_qa.yaml | ✅ 120s | ✅ 120s | ✅ 180s | 已同步 |
| config_prod.yaml | ✅ 120s | ✅ 120s | ✅ 180s | 已同步 |

## 代码中的超时使用

### 1. Volc API 使用

```go
// internal/numind/biz/volc/volc.go
timeout := viper.GetDuration("volc.timeout")
if timeout == 0 {
    timeout = 120 * time.Second // 默认值
}

client := &http.Client{
    Timeout: timeout,
    Transport: &http.Transport{
        ResponseHeaderTimeout: timeout, // 与整体超时一致
    },
}
```

### 2. 阿里云 API 使用

```go
// internal/numind/biz/ali/ali.go
timeout := viper.GetDuration("ali.text.timeout")
if timeout == 0 {
    timeout = 120 * time.Second // 默认值
}

client := &http.Client{
    Timeout: timeout,
    Transport: &http.Transport{
        ResponseHeaderTimeout: timeout, // 与整体超时一致
    },
}
```

## 环境差异说明

### 开发环境 (local/dev)
- 超时时间相对较长，便于调试和开发
- 网络环境可能不稳定，需要容错

### 测试环境 (qa)
- 超时时间适中，平衡性能和稳定性
- 用于功能测试和性能验证

### 生产环境 (prod)
- 超时时间合理，确保用户体验
- 网络环境相对稳定，但需要容错

## 最佳实践

### 1. 超时设置原则
- **文本生成**: 120秒（适合复杂文本处理）
- **图像生成**: 180秒（适合AI图像生成）
- **网络请求**: 根据网络环境调整
- **用户体验**: 避免过短超时导致失败

### 2. 配置管理
- 所有超时设置放在配置文件中
- 不同环境使用相同的超时策略
- 支持运行时调整（通过配置文件）
- 提供合理的默认值

### 3. 监控和调优
- 监控API调用成功率
- 分析超时失败的原因
- 根据实际使用情况调整超时时间
- 记录超时相关的日志信息

## 验证方法

### 1. 配置文件检查
```bash
# 检查所有配置文件中的超时设置
grep -r "timeout:" config_*.yaml
```

### 2. 服务启动验证
- 启动服务时检查配置加载
- 确认超时设置正确应用
- 查看启动日志中的配置信息

### 3. API调用测试
- 测试volc API的超时行为
- 测试千问API的超时行为
- 测试万象API的超时行为
- 验证超时设置是否生效

## 总结

通过这次配置同步，实现了：

1. **统一性**: 所有环境使用相同的超时策略
2. **可配置性**: 超时时间可以通过配置文件调整
3. **稳定性**: 合理的超时设置提高API调用成功率
4. **维护性**: 集中管理超时配置，便于维护和调整

现在所有环境的配置文件都已同步，qianwen、wanxiang、volc的超时时间配置完整且一致。
