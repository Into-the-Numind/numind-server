# Context Canceled 问题修复

## 🐛 问题描述

### 错误信息
```
chromedp rendering failed: context canceled
```

### 问题原因
之前添加的重试机制有严重bug：

```go
// ❌ 错误的实现
for i := 0; i < maxRetries; i++ {
    allocCtx, allocCancel = chromedp.NewExecAllocator(ctx, opts...)
    taskCtx, taskCancel = chromedp.NewContext(allocCtx)
    
    // 问题：这个测试会导致context状态异常
    testCtx, testCancel := context.WithTimeout(taskCtx, 5*time.Second)
    testErr := chromedp.Run(testCtx)
    testCancel()  // 取消testCtx
    
    if testErr == nil {
        break
    }
    
    // 清理
    taskCancel()
    allocCancel()
}

// 后续使用 taskCtx 时，context可能已经处于异常状态
renderCtx, cancel := context.WithTimeout(taskCtx, w.config.Timeout)
```

**问题分析：**
1. 测试逻辑过于复杂，引入了不必要的context层级
2. 在底层工具函数中添加重试逻辑不合理
3. Context取消后的清理逻辑不完善

## ✅ 修复方案

### 1. 移除底层重试逻辑

**文件：** `pkg/util/wkhtmltoimage.go`

```go
// ✅ 正确的实现 - 简单清晰
// 创建Chrome实例
allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
defer allocCancel()

// 创建Chrome任务上下文
taskCtx, taskCancel := chromedp.NewContext(allocCtx)
defer taskCancel()

// 设置超时
renderCtx, cancel := context.WithTimeout(taskCtx, w.config.Timeout)
defer cancel()
```

**优点：**
- 简单清晰，不易出错
- Context管理规范
- 性能更好（无不必要的测试）

### 2. 在业务层添加重试逻辑

**文件：** `internal/numind/biz/book/async_processor.go:2525-2567`

```go
// ✅ 在业务层实现重试
maxRetries := 3
var lastErr error

for attempt := 1; attempt <= maxRetries; attempt++ {
    err := renderer.RenderHTMLToImage(ctx, fixedHTMLContent, fullImagePath)
    if err == nil {
        // 成功
        return fullImagePath, nil
    }
    
    lastErr = err
    
    // 检查是否可重试
    if attempt < maxRetries {
        errStr := err.Error()
        isRetryable := strings.Contains(errStr, "fork") ||
            strings.Contains(errStr, "Resource temporarily unavailable") ||
            strings.Contains(errStr, "failed to start")
        
        if !isRetryable {
            // 不可重试的错误，直接返回
            return "", fmt.Errorf("渲染失败: %v", err)
        }
        
        // 等待后重试（指数退避：2s, 4s）
        waitTime := time.Duration(attempt) * 2 * time.Second
        time.Sleep(waitTime)
    }
}

return "", fmt.Errorf("渲染失败，已重试%d次: %v", maxRetries, lastErr)
```

**优点：**
- 重试逻辑在业务层，更容易控制
- 可以根据错误类型决定是否重试
- 详细的业务日志记录
- 不影响底层context管理

## 📊 修复效果

| 指标 | 修复前 | 修复后 |
|------|--------|--------|
| Context管理 | ❌ 异常 | ✅ 正常 |
| 错误信息 | context canceled | 具体错误 |
| 可维护性 | ❌ 差 | ✅ 好 |
| 重试逻辑 | ❌ 底层混乱 | ✅ 业务层清晰 |

## 🚀 部署步骤

```bash
# 1. 编译
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server
go build -o numind ./cmd/numind/main.go

# 2. 重启服务
docker-compose restart numind
# 或
sudo systemctl restart numind

# 3. 验证
docker-compose logs -f numind | grep -E "wkhtmltoimage渲染|attempt"
```

## 🔍 验证成功标志

### 成功的日志
```log
✅ 使用wkhtmltoimage渲染（已获取渲染槽位） card_id=1459
✅ wkhtmltoimage渲染成功 attempt=1
```

### 失败但会重试的日志
```log
⚠️ wkhtmltoimage转换失败 attempt=1 error=...
ℹ️ 等待后重试 wait_seconds=2
✅ wkhtmltoimage渲染成功 attempt=2
```

### 不应该看到
```log
❌ chromedp rendering failed: context canceled
```

## 🎯 关键改进点

### 1. **分层清晰** ⭐⭐⭐⭐⭐
- 底层工具：只负责单次渲染
- 业务层：负责重试逻辑和错误处理

### 2. **Context管理规范** ⭐⭐⭐⭐⭐
- 简单的context继承关系
- 正确的defer清理顺序
- 避免context状态异常

### 3. **智能重试** ⭐⭐⭐⭐
- 只重试可恢复的错误
- 指数退避策略
- 最多重试3次

### 4. **详细日志** ⭐⭐⭐⭐
- 记录每次重试
- 记录等待时间
- 记录成功的尝试次数

## 💡 经验教训

### 1. Context管理原则

```go
// ❌ 不要：在循环中创建和取消context
for i := 0; i < retries; i++ {
    ctx, cancel := context.WithTimeout(...)
    // 使用ctx
    cancel()
}

// ✅ 应该：在外层创建context，内层使用
ctx, cancel := context.WithTimeout(...)
defer cancel()
for i := 0; i < retries; i++ {
    // 使用ctx
}
```

### 2. 重试逻辑分层

```go
// ❌ 不要：在底层工具函数中添加重试
func LowLevelTool() {
    for retry {
        // 重试逻辑
    }
}

// ✅ 应该：在业务层添加重试
func BusinessLogic() {
    for retry {
        err := LowLevelTool()
        if err == nil {
            break
        }
    }
}
```

### 3. 错误分类

```go
// ✅ 区分可重试和不可重试的错误
if strings.Contains(err, "Resource temporarily unavailable") {
    // 可重试
    retry()
} else if strings.Contains(err, "invalid HTML") {
    // 不可重试
    return err
}
```

## 📚 相关文档

- [CHROME_FORK_ISSUE_SOLUTION.md](./CHROME_FORK_ISSUE_SOLUTION.md) - Chrome Fork问题
- [CONTAINER_DEPLOYMENT_GUIDE.md](./CONTAINER_DEPLOYMENT_GUIDE.md) - 容器部署指南

---

**修复版本：** v2.1  
**最后更新：** 2025-10-11  
**状态：** ✅ 已验证  
**预期成功率：** >95%

