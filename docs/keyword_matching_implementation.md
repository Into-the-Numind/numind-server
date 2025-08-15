# 关键词匹配功能实现文档

## 概述

本文档描述了在Numind Server中实现的**方案一（关键词匹配与分词技术）**的完整实现。该功能通过中文分词技术，实现了基于用户查询的智能书籍搜索功能。

## 功能特性

- ✅ 中文分词：使用jieba分词库进行准确的中文分词
- ✅ 关键词匹配：基于标题和标签的智能匹配
- ✅ 分数排序：根据匹配度对搜索结果进行排序
- ✅ WebSocket集成：支持实时搜索请求
- ✅ 数据库优化：添加复合索引提升查询性能

## 技术架构

### 1. 核心组件

#### 关键词匹配器 (`internal/pkg/util/keyword_matcher.go`)
- 基于jieba分词库的中文分词
- 停用词过滤
- 匹配分数计算

#### 搜索服务 (`internal/numind/biz/book/search.go`)
- 书籍搜索业务逻辑
- 结果排序和分页
- 性能优化

#### 聊天业务逻辑 (`internal/numind/biz/chat/chat.go`)
- WebSocket消息处理
- 搜索请求路由
- 结果返回

### 2. 数据模型

#### BookM模型增强
```go
type BookM struct {
    gorm.Model
    Title  string `gorm:"size:255;not null;index:idx_title_tags" json:"title"`
    Tags   string `gorm:"size:255;index:idx_title_tags" json:"tags"`
    Status string `gorm:"size:20;default:'creating';index" json:"status"`
    // ... 其他字段
}
```

#### 新增索引
- `idx_title_tags`: Title和Tags字段的复合索引
- `idx_status`: Status字段索引，优化状态过滤

## 使用方法

### 1. WebSocket搜索

#### 发送搜索请求
```json
{
  "type": "search_books",
  "content": "旅行照片卡册",
  "timestamp": "2024-01-01T12:00:00Z"
}
```

#### 接收搜索结果
```json
{
  "type": "search_books_result",
  "content": "找到 3 本相关书籍",
  "data": [
    {
      "id": 1,
      "title": "旅行照片卡册",
      "tags": "旅行,摄影,回忆",
      "category_id": 1,
      "image_url": "https://...",
      "card_count": 12
    }
  ],
  "timestamp": "2024-01-01T12:00:01Z"
}
```

### 2. 程序化使用

#### 创建关键词匹配器
```go
import "numind-server/internal/pkg/util"

matcher := util.NewKeywordMatcher()
defer matcher.Close()
```

#### 提取关键词
```go
keywords := matcher.GetKeywords("旅行照片卡册")
// 结果: ["旅行", "照片", "卡册"]
```

#### 计算匹配分数
```go
score := matcher.MatchScore(keywords, "旅行照片卡册", "旅行,摄影,回忆")
// 结果: 3 (匹配3个关键词)
```

#### 使用搜索服务
```go
import "numind-server/internal/numind/biz/book"

searchService := book.NewSearchService()
defer searchService.Close()

results := searchService.SearchBooks(ctx, "旅行照片卡册", books, 5)
```

## 算法原理

### 1. 分词算法
- 使用jieba分词库进行中文分词
- 支持自定义词典和停用词
- 过滤单字符和无意义词汇

### 2. 匹配算法
1. **关键词提取**：对用户查询和书籍信息进行分词
2. **标签处理**：将逗号分隔的标签进行分词
3. **匹配计算**：计算用户关键词与书籍关键词的重合度
4. **分数排序**：按匹配分数降序排列

### 3. 性能优化
- 使用map进行快速关键词查找
- 复合索引优化数据库查询
- 结果缓存和分页处理

## 配置说明

### 1. 依赖管理
```bash
go get github.com/yanyiwu/gojieba
```

### 2. 停用词配置
在 `internal/pkg/util/keyword_matcher.go` 中配置停用词：
```go
var stopWords = map[string]bool{
    "的": true, "了": true, "在": true, "是": true,
    // ... 更多停用词
}
```

### 3. 数据库索引
```sql
-- 复合索引
CREATE INDEX idx_title_tags ON book(title, tags);

-- 状态索引
CREATE INDEX idx_status ON book(status);
```

## 测试验证

### 1. 单元测试
```bash
cd internal/pkg/util
go test -v
```

### 2. 集成测试
```bash
cd examples
go run keyword_matching_example.go
```

### 3. 性能测试
```bash
# 测试分词性能
go test -bench=BenchmarkGetKeywords

# 测试匹配性能
go test -bench=BenchmarkMatchScore
```

## 部署注意事项

### 1. 内存管理
- jieba分词器会占用一定内存
- 建议在生产环境中使用连接池管理分词器实例

### 2. 性能监控
- 监控搜索响应时间
- 监控数据库查询性能
- 监控内存使用情况

### 3. 扩展性考虑
- 支持分布式搜索
- 集成Elasticsearch等搜索引擎
- 实现搜索结果缓存

## 故障排除

### 1. 常见问题

#### 分词结果不准确
- 检查jieba词典版本
- 更新自定义词典
- 调整停用词配置

#### 搜索性能慢
- 检查数据库索引
- 优化查询语句
- 考虑结果缓存

#### 内存占用高
- 检查分词器实例管理
- 优化数据结构
- 监控内存使用

### 2. 日志分析
```go
log.C(ctx).Infow("Book search completed", 
    "query", userQuery, 
    "total_books", len(books), 
    "result_count", len(result),
    "top_score", topScore)
```

## 未来改进

### 1. 功能增强
- 支持模糊匹配
- 添加权重配置
- 支持多语言搜索

### 2. 性能优化
- 实现搜索结果缓存
- 优化分词算法
- 支持异步搜索

### 3. 用户体验
- 搜索建议
- 热门搜索
- 搜索历史

## 总结

关键词匹配功能已经成功实现并集成到WebSocket聊天系统中。该功能提供了：

1. **准确的中文分词**：基于jieba分词库
2. **智能的匹配算法**：综合考虑标题和标签
3. **高效的搜索服务**：支持实时搜索和结果排序
4. **良好的扩展性**：易于集成和扩展

该实现为小程序提供了强大的书籍搜索能力，用户可以快速找到相关的卡册内容。
