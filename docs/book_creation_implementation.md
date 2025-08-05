# 书籍创建API重新实现

## 需求说明

小程序端请求创建卡册的API后，返回的数据结构是 `BookM`，同时分页后的卡片数据以JSON格式存储在 `CardM.ProcessedText` 字段中。

## 实现方案

### 1. API响应结构

创建卡册API现在返回完整的 `BookM` 结构：

```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 15,
    "user_id": 1,
    "title": "AI生成卡册 - 2024-01-15 10:30:45",
    "template_id": "1",
    "card_count": 3,
    "view_time": "2024-01-15T10:30:45.123Z",
    "created_at": "2024-01-15T10:30:45.123Z",
    "updated_at": "2024-01-15T10:30:45.123Z"
  }
}
```

### 2. 分页数据存储

分页后的卡片数据以JSON格式存储在 `CardM.ProcessedText` 字段中：

```json
[
  [
    {
      "type": "number",
      "content": "AI处理结果"
    },
    {
      "type": "body", 
      "content": "未来竞争力：联机时代的独立思考与认知进化\n\n---\n\n### 一、联机的独立思考者\n你可以联机打游戏，参考他人的攻略通关，但最终仍需独立完成这一关，达成自己的期待。"
    }
  ],
  [
    {
      "type": "body",
      "content": "这恰如跨国企业的\"全球本土化\"（glocal）战略——保持全球视野，同时守护在地特色。若拥有独立思考能力，联机思考将使思想质量更高、迭代更快。"
    }
  ],
  [
    {
      "type": "body",
      "content": "### 二、未来职业的通用竞争力\n在人工智能盛行、行业边界消融的今天，未来的核心竞争力在于：\n- 用机器学习和处理信息\n- 用大脑整合并创新思想\n- 用系统思维解决问题"
    }
  ]
]
```

### 3. 书籍详情API

获取书籍详情时，返回 `BookM` 结构加上分页卡片数据：

```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 15,
    "user_id": 1,
    "title": "AI生成卡册 - 2024-01-15 10:30:45",
    "template_id": "1",
    "card_count": 3,
    "view_time": "2024-01-15T10:30:45.123Z",
    "created_at": "2024-01-15T10:30:45.123Z",
    "updated_at": "2024-01-15T10:30:45.123Z",
    "paginated_cards": [
      [
        {
          "type": "number",
          "content": "AI处理结果"
        },
        {
          "type": "body",
          "content": "未来竞争力：联机时代的独立思考与认知进化\n\n---\n\n### 一、联机的独立思考者\n你可以联机打游戏，参考他人的攻略通关，但最终仍需独立完成这一关，达成自己的期待。"
        }
      ],
      [
        {
          "type": "body",
          "content": "这恰如跨国企业的\"全球本土化\"（glocal）战略——保持全球视野，同时守护在地特色。若拥有独立思考能力，联机思考将使思想质量更高、迭代更快。"
        }
      ]
    ]
  }
}
```

## 核心代码实现

### 1. 创建卡册接口

```go
// Create 创建一本卡册
func (ctrl *BookController) Create(c *gin.Context) {
    // ... 处理请求参数和AI文本处理 ...
    
    // 使用分页引擎处理文本
    paginationBiz := pagination.NewPaginationBiz()
    elements := []pagination.Element{
        {
            Type:    pagination.ElementTypeNumber,
            Content: "AI处理结果",
        },
        {
            Type:    pagination.ElementTypeBody,
            Content: qianwenResult,
        },
    }
    
    paginatedContent, err := paginationBiz.PaginateElements(elements)
    if err != nil {
        // 处理错误
    }
    
    // 创建卡册
    book := &model.BookM{
        UserID:     userID,
        Title:      fmt.Sprintf("AI生成卡册 - %s", time.Now().Format("2006-01-02 15:04:05")),
        TemplateID: req.TemplateID,
        ViewTime:   &now,
    }
    
    if err := ctrl.b.Books().Create(c, book); err != nil {
        // 处理错误
    }
    
    // 将分页卡片数据转换为JSON格式
    var cardsJSON []interface{}
    for _, card := range paginatedContent.Cards {
        var cardElements []map[string]interface{}
        for _, element := range card.Elements {
            cardElements = append(cardElements, map[string]interface{}{
                "type":    element.Type,
                "content": element.Content,
            })
        }
        cardsJSON = append(cardsJSON, cardElements)
    }
    
    // 将JSON数据转换为字符串
    cardsJSONStr, err := json.Marshal(cardsJSON)
    if err != nil {
        // 处理错误
    }
    
    // 创建卡片记录，将分页数据存储到ProcessedText字段
    card := &model.CardM{
        UserID:        userID,
        BookID:        book.ID,
        ImageID:       0,
        ProcessedText: string(cardsJSONStr), // 将JSON数据存储到ProcessedText字段
        SortOrder:     1, // 从1开始计数
    }
    
    if err := ctrl.b.Cards().Create(c, card); err != nil {
        // 处理错误
    }
    
    // 更新书籍的卡片数量
    book.CardCount = len(paginatedContent.Cards)
    if err := ctrl.b.Books().Update(c, book); err != nil {
        // 处理错误
    }
    
    // 返回BookM结构
    core.WriteResponse(c, nil, book)
}
```

### 2. 书籍详情接口

```go
// GetBookResponse 书籍详情响应结构
type GetBookResponse struct {
    *model.BookM
    PaginatedCards []interface{} `json:"paginated_cards,omitempty"`
}

// Get 获取一本卡册的详细信息
func (ctrl *BookController) Get(c *gin.Context) {
    // ... 获取书籍信息 ...
    
    // 获取该书籍的所有卡片
    _, cards, err := ctrl.b.Cards().ListByBook(c, uint(bookID), 0, 1000)
    if err != nil {
        // 处理错误
    }
    
    // 解析卡片中的JSON数据
    var paginatedCards []interface{}
    for _, card := range cards {
        if card.ProcessedText != "" {
            var cardData []interface{}
            if err := json.Unmarshal([]byte(card.ProcessedText), &cardData); err != nil {
                // 处理错误
                continue
            }
            paginatedCards = append(paginatedCards, cardData)
        }
    }
    
    response := &GetBookResponse{
        BookM:          book,
        PaginatedCards: paginatedCards,
    }
    
    core.WriteResponse(c, nil, response)
}
```

## 数据流程

1. **用户请求创建卡册** → 发送文本和模板ID
2. **AI处理文本** → 使用千问模型处理文本
3. **分页处理** → 使用分页引擎将文本分割成多个卡片
4. **创建书籍记录** → 在数据库中创建BookM记录
5. **创建卡片记录** → 将分页后的JSON数据存储到CardM.ProcessedText字段
6. **更新书籍信息** → 更新书籍的卡片数量
7. **返回BookM** → 返回完整的BookM结构

## 测试方法

1. 创建卡册：
```bash
curl -X POST http://localhost:8080/v1/books \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "text": "你的文本内容",
    "template_id": "1"
  }'
```

2. 获取书籍详情：
```bash
curl -X GET http://localhost:8080/v1/books/{book_id} \
  -H "Authorization: Bearer YOUR_TOKEN"
```

## 优势

1. **数据结构清晰**：返回标准的BookM结构
2. **数据存储高效**：将分页数据以JSON格式存储在现有字段中
3. **向后兼容**：不影响现有的数据库结构
4. **易于扩展**：可以轻松添加更多字段和功能
5. **前端友好**：返回的数据结构便于前端处理 