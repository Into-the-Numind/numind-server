# Feedback 类型字段说明

## 概述

Feedback表已经包含了一个`Type`字段，用于分类不同类型的用户反馈。该字段为string类型，长度为50个字符。

## 字段定义

### 数据库字段
```go
Type string `gorm:"size:50;not null" json:"type"`
```

### API字段
```go
// CreateFeedbackRequest 创建反馈的请求参数
type CreateFeedbackRequest struct {
    Content string `json:"content" binding:"required" valid:"required,stringlength(1|1000)"`
    Type    string `json:"type"`
}

// FeedbackResponse 反馈的响应参数
type FeedbackResponse struct {
    ID        uint      `json:"id"`
    UserID    uint      `json:"user_id"`
    Content   string    `json:"content"`
    Type      string    `json:"type"`
    Status    int       `json:"status"`
    Reply     string    `json:"reply"`
    CreatedAt string    `json:"created_at"`
    UpdatedAt string    `json:"updated_at"`
    User      *UserInfo `json:"user,omitempty"`
}
```

## 类型常量

定义了以下反馈类型常量：

```go
const (
    FeedbackTypeBug        = "bug"        // 问题反馈
    FeedbackTypeFeature    = "feature"    // 功能建议
    FeedbackTypeImprovement = "improvement" // 改进建议
    FeedbackTypeOther      = "other"      // 其他
)
```

## 使用方式

### 1. 创建反馈时指定类型
```go
feedback := &model.Feedback{
    UserID:  userID,
    Content: req.Content,
    Type:    req.Type,  // 使用前端传入的类型
    Status:  0,         // 默认状态为待处理
}
```

### 2. 前端使用示例
```javascript
// 创建反馈
const createFeedback = {
    content: "希望添加新的功能",
    type: "feature"  // 使用预定义的类型常量
}

// 获取反馈列表时，可以根据类型筛选
const feedbacks = await api.getFeedbacks({
    type: "bug"  // 只获取问题反馈
})
```

## 类型说明

| 类型 | 常量值 | 说明 |
|------|--------|------|
| 问题反馈 | `bug` | 用户报告的问题、错误 |
| 功能建议 | `feature` | 用户提出的新功能需求 |
| 改进建议 | `improvement` | 对现有功能的改进建议 |
| 其他 | `other` | 其他类型的反馈 |

## 扩展性

如果需要添加新的反馈类型，只需要：

1. 在常量定义中添加新的类型
2. 更新前端的选择列表
3. 在管理后台添加相应的处理逻辑

## 数据库迁移

由于`Type`字段已经存在，不需要进行数据库迁移。字段定义为：
- 类型：`VARCHAR(50)`
- 约束：`NOT NULL`
- 索引：无（可根据需要添加）

## 注意事项

1. 前端应该使用预定义的常量值，避免使用自定义字符串
2. 类型字段是必填的，创建反馈时必须指定
3. 可以根据类型进行统计和分析，了解用户反馈的分布情况
