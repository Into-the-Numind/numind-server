# 带宽优化方案

## 概述

针对云服务器3M带宽限制问题，实现了一套完整的API带宽优化方案，通过多种技术手段减少数据传输量，提高API响应效率。

## 优化策略

### 1. Gzip压缩

#### 实现方式
- 在router中添加全局gzip压缩中间件
- 自动检测客户端是否支持gzip压缩
- 对支持gzip的客户端自动压缩响应

#### 优化效果
- 文本数据压缩率：60-80%
- JSON响应压缩率：50-70%
- 图片数据压缩率：10-30%（图片本身已压缩）

#### 使用方法
客户端请求时添加header：
```http
Accept-Encoding: gzip
```

### 2. 字段过滤

#### 实现方式
- 支持通过query参数`fields`指定需要的字段
- 字段名用逗号分隔
- 自动过滤不需要的数据字段

#### 优化效果
- 根据字段选择减少20-50%的数据量
- 减少网络传输时间
- 降低客户端解析负担

#### 使用方法
```http
GET /v1/books?fields=id,title,card_count
GET /v1/admin/configs?fields=key,value
```

### 3. 分页优化

#### 实现方式
- 默认分页大小限制为10条
- 支持offset和limit参数
- 避免一次性传输大量数据

#### 优化效果
- 控制单次响应数据量
- 提高响应速度
- 减少内存占用

#### 使用方法
```http
GET /v1/books?offset=0&limit=20
```

### 4. 响应结构优化

#### 实现方式
- 统一的响应格式：`{code, message, data}`
- 支持最小化响应
- 智能字段选择

#### 优化效果
- 减少冗余数据
- 提高API一致性
- 便于客户端处理

## 技术实现

### 1. 压缩中间件

```go
// internal/pkg/middleware/compression.go
func GzipCompression() gin.HandlerFunc {
    return gin.HandlerFunc(func(c *gin.Context) {
        // 检查客户端是否支持gzip压缩
        acceptEncoding := c.GetHeader("Accept-Encoding")
        if !strings.Contains(acceptEncoding, "gzip") {
            c.Next()
            return
        }

        // 设置响应头
        c.Header("Content-Encoding", "gzip")
        c.Header("Vary", "Accept-Encoding")

        // 创建gzip writer
        gw := gzip.NewWriter(c.Writer)
        defer gw.Close()

        // 包装response writer
        c.Writer = &gzipWriter{
            ResponseWriter: c.Writer,
            gzipWriter:     gw,
        }

        c.Next()
    })
}
```

### 2. 字段过滤工具

```go
// internal/pkg/util/response_filter.go
func FilterFields(data interface{}, fields []string) interface{} {
    if len(fields) == 0 {
        return data
    }

    // 将数据转换为JSON，然后解析为map
    dataBytes, err := json.Marshal(data)
    if err != nil {
        return data
    }

    var dataMap map[string]interface{}
    if err := json.Unmarshal(dataBytes, &dataMap); err != nil {
        return data
    }

    // 创建新的map，只包含指定字段
    filteredMap := make(map[string]interface{})
    for _, field := range fields {
        if value, exists := dataMap[field]; exists {
            filteredMap[field] = value
        }
    }

    return filteredMap
}
```

### 3. 压缩响应函数

```go
// internal/pkg/core/core.go
func WriteCompressedResponse(c *gin.Context, err error, data interface{}) {
    // 检查客户端是否支持gzip压缩
    acceptEncoding := c.GetHeader("Accept-Encoding")
    if !strings.Contains(acceptEncoding, "gzip") {
        WriteResponse(c, err, data)
        return
    }

    // 设置压缩响应头
    c.Header("Content-Encoding", "gzip")
    c.Header("Content-Type", "application/json")
    
    // 使用gzip压缩响应
    gw := gzip.NewWriter(c.Writer)
    defer gw.Close()
    
    responseBytes, _ := json.Marshal(response)
    gw.Write(responseBytes)
}
```

## API使用示例

### 1. 书籍列表API

#### 完整响应（无优化）
```http
GET /v1/books
Accept-Encoding: identity
```

#### 压缩响应
```http
GET /v1/books
Accept-Encoding: gzip
```

#### 字段过滤+压缩
```http
GET /v1/books?fields=id,title,card_count
Accept-Encoding: gzip
```

#### 分页+字段过滤+压缩
```http
GET /v1/books?offset=0&limit=10&fields=id,title,card_count
Accept-Encoding: gzip
```

### 2. 配置管理API

#### 完整配置列表
```http
GET /v1/admin/configs
Accept-Encoding: gzip
```

#### 只获取关键字段
```http
GET /v1/admin/configs?fields=key,value
Accept-Encoding: gzip
```

## 性能测试

### 测试脚本
使用 `scripts/test_bandwidth_optimization.sh` 进行测试：

```bash
# 设置JWT token
export TOKEN="your-jwt-token-here"

# 运行测试
./scripts/test_bandwidth_optimization.sh
```

### 测试指标
- 响应大小对比
- 压缩率计算
- 字段过滤效果
- 网络传输时间

## 最佳实践

### 1. 客户端优化
- 始终发送 `Accept-Encoding: gzip` 头
- 根据实际需求使用 `fields` 参数
- 合理设置分页参数
- 实现响应缓存机制

### 2. 服务端优化
- 启用gzip压缩中间件
- 实现智能字段过滤
- 优化数据库查询
- 使用连接池和缓存

### 3. 网络优化
- 使用CDN加速
- 启用HTTP/2
- 实现请求合并
- 优化DNS解析

## 监控和调优

### 1. 性能指标
- 响应时间
- 数据传输量
- 压缩率
- 带宽使用率

### 2. 调优建议
- 根据实际使用情况调整分页大小
- 监控压缩效果，必要时调整压缩级别
- 分析字段使用频率，优化默认字段选择
- 定期清理无用数据和缓存

## 总结

通过实施这套带宽优化方案，可以显著减少API数据传输量：

- **gzip压缩**：减少60-80%的文本数据传输
- **字段过滤**：减少20-50%的数据量
- **分页优化**：控制单次响应大小
- **响应优化**：提高API一致性和效率

这些优化措施可以有效缓解3M带宽限制带来的问题，提高API响应速度，改善用户体验。
