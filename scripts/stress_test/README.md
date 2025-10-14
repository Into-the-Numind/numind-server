# 创建Book API 压力测试工具

## 功能说明

这是一个用于测试创建Book API性能的压力测试工具，主要功能包括：

1. 自动进行微信登录获取认证token
2. 循环100次调用创建Book API
3. 统计成功率、响应时间等性能指标
4. 实时显示测试进度

## 配置说明

测试配置位于 `stress_test_book.go` 文件的常量定义中：

```go
const (
    // API端点配置
    baseURL       = "https://youshu.asia/dev"
    loginAPI      = "/v1/wechat/login"
    createBookAPI = "/v1/books"

    // 测试配置
    testCount = 100              // 测试次数
    timeout   = 30 * time.Second // 请求超时时间
)
```

您可以根据需要修改：
- `testCount`: 调整测试次数
- `timeout`: 调整请求超时时间
- `baseURL`: 修改目标服务器地址

## 使用方法

### 编译

```bash
cd scripts/stress_test
go build -o stress_test_book
```

### 运行

```bash
./stress_test_book
```

或者直接运行（无需编译）：

```bash
go run stress_test_book.go
```

## 输出示例

```
开始压力测试...
目标API: https://youshu.asia/dev/v1/books
测试次数: 100

步骤1: 获取登录token...
✓ 登录成功，获得token: eyJhbGciOiJIUzI1NiIsInR5cCI...

步骤2: 开始压力测试...
进度: [100/100] 100.00% 

总测试时间: 45.234s

======================================================================
压力测试结果统计
======================================================================
总请求数:       100
成功数:         98 (98.00%)
失败数:         2 (2.00%)
----------------------------------------------------------------------
总耗时:         44.123s
平均响应时间:   441ms
最短响应时间:   123ms
最长响应时间:   1.234s
======================================================================

QPS (每秒请求数): 2.21
```

## 测试数据

每次请求会发送以下格式的数据：

```json
{
    "text": "随机生成的100-500字的中文测试文本...",
    "template_id": "1"
}
```

- `text`: 随机生成的100-500字中文文本，内容涵盖生活、学习、艺术、健康等多个主题
- `template_id`: 使用默认模板ID "1"

**随机文本特点：**
- 每次请求的文本长度在100-500字之间随机变化
- 文本由多个预设的段落随机组合而成
- 内容真实自然，适合测试文本处理能力
- 包含中文标点符号和换行，模拟真实使用场景

## 自定义测试数据

### 修改文本长度范围

如果需要修改随机文本的字数范围，可以编辑 `main()` 函数中的 `generateRandomText` 调用：

```go
// 生成100-500字的随机文本
randomText := generateRandomText(100, 500)

// 或者修改为其他范围，例如：
randomText := generateRandomText(200, 1000)  // 200-1000字
randomText := generateRandomText(50, 200)    // 50-200字
```

### 修改模板ID

修改 `CreateBookRequest` 中的 `TemplateID`：

```go
bookRequest := CreateBookRequest{
    Text:       randomText,
    TemplateID: "2", // 修改为您需要的模板ID
}
```

### 自定义文本内容库

如果想要修改文本段落库，可以编辑 `generateRandomText()` 函数中的 `paragraphs` 数组，添加或替换您自己的文本片段。

## 并发测试

当前版本为串行测试。如需并发测试，可以取消注释以下代码并添加goroutine：

```go
// 在循环前设置并发数
var wg sync.WaitGroup
concurrency := 10 // 并发数

// 使用信号量控制并发
semaphore := make(chan struct{}, concurrency)

for i := 1; i <= testCount; i++ {
    wg.Add(1)
    semaphore <- struct{}{} // 获取信号量
    
    go func(index int) {
        defer wg.Done()
        defer func() { <-semaphore }() // 释放信号量
        
        // 测试逻辑
        ...
    }(i)
}

wg.Wait()
```

## 注意事项

1. 确保目标服务器可以访问
2. 测试前确认登录凭证有效（当前使用code: "98"）
3. 大量请求可能会触发服务器限流，请适当调整测试次数
4. 建议在测试环境中运行，避免影响生产环境

## 故障排查

### 登录失败

如果登录失败，请检查：
- 网络连接是否正常
- 登录API地址是否正确
- code参数是否有效

### 请求超时

如果出现大量超时：
- 增加 `timeout` 配置
- 检查服务器负载
- 减少 `testCount` 降低测试强度

### 所有请求失败

检查：
- token是否正确获取
- API端点路径是否正确
- Authorization header格式是否正确

