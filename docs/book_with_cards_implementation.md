# 包含cards字段的书籍创建实现

## 需求说明

用户希望在创建卡册API返回的 `BookM` 结构中包含一个 `cards` 数组字段，让前端知道这个书籍渲染了多少张卡片，便于小程序端做渲染。

## 实现方案

### 1. 修改书籍存储层

在 `GetByID` 方法中添加 `Cards` 关联数据的预加载：

```go
func (s *books) GetByID(ctx context.Context, id uint) (*model.BookM, error) {
	var book model.BookM
	err := s.db.WithContext(ctx).Preload("Category").Preload("Cards").First(&book, id).Error
	if err != nil {
		return nil, err
	}
	return &book, nil
}
```

### 2. 修改卡片模型

添加自定义JSON序列化方法，将 `ProcessedText` 字段中的JSON字符串解析为对象：

```go
// MarshalJSON 自定义JSON序列化，将ProcessedText解析为JSON对象
func (c *CardM) MarshalJSON() ([]byte, error) {
	type Alias CardM
	
	// 尝试解析ProcessedText为JSON
	var parsedData interface{}
	if c.ProcessedText != "" {
		if err := json.Unmarshal([]byte(c.ProcessedText), &parsedData); err == nil {
			// 如果解析成功，创建一个新的结构体，将解析后的数据作为processed_text返回
			alias := &struct {
				*Alias
				ProcessedText interface{} `json:"processed_text"`
			}{
				Alias:         (*Alias)(c),
				ProcessedText: parsedData,
			}
			return json.Marshal(alias)
		}
	}
	
	// 如果解析失败或为空，使用原始结构体
	return json.Marshal((*Alias)(c))
}
```

### 3. 修改创建卡册接口

在创建卡册后，获取包含卡片数据的完整书籍信息：

```go
// 获取更新后的书籍信息，包含卡片数据
updatedBook, err := ctrl.b.Books().GetByID(c, book.ID)
if err != nil {
	log.C(c).Errorw("Failed to get updated book", "error", err.Error())
	// 如果获取失败，返回原始书籍信息
	core.WriteResponse(c, nil, book)
	return
}

// 返回包含卡片数据的BookM结构
core.WriteResponse(c, nil, updatedBook)
```

## API响应结构

### 创建卡册API返回

```json
{
  "code": 0,
  "message": "",
  "data": {
    "ID": 16,
    "CreatedAt": "2025-08-04T17:16:22.95+08:00",
    "UpdatedAt": "2025-08-04T17:16:22.96+08:00",
    "DeletedAt": null,
    "user_id": 1,
    "title": "AI生成卡册 - 2025-08-04 17:16:22",
    "category_id": null,
    "category_name": "",
    "template_id": "1",
    "tags": "",
    "card_count": 3,
    "view_time": "2025-08-04T17:16:22.944145+08:00",
    "cards": [
      {
        "ID": 1,
        "CreatedAt": "2025-08-04T17:16:22.95+08:00",
        "UpdatedAt": "2025-08-04T17:16:22.96+08:00",
        "DeletedAt": null,
        "user_id": 1,
        "book_id": 16,
        "image_id": 0,
        "processed_text": [
          [
            {
              "type": "number",
              "content": "AI处理结果"
            },
            {
              "type": "body",
              "content": "未来竞争力：联机时代的独立思考与认知进化\n\n---\n\n### 一、联机的独立思考者\n你可以联机打游戏，参考他人的攻略通关，但最终仍需独立完成这一关，达成自己的期待。"
            }
          ]
        ],
        "sort_order": 0,
        "tags": ""
      }
    ]
  }
}
```

### 书籍详情API返回

```json
{
  "code": 0,
  "message": "",
  "data": {
    "id": 16,
    "user_id": 1,
    "title": "AI生成卡册 - 2025-08-04 17:16:22",
    "template_id": "1",
    "card_count": 3,
    "view_time": "2025-08-04T17:16:22.944145+08:00",
    "created_at": "2025-08-04T17:16:22.95+08:00",
    "updated_at": "2025-08-04T17:16:22.96+08:00",
    "cards": [
      {
        "id": 1,
        "user_id": 1,
        "book_id": 16,
        "image_id": 0,
        "processed_text": [
          [
            {
              "type": "number",
              "content": "AI处理结果"
            },
            {
              "type": "body",
              "content": "未来竞争力：联机时代的独立思考与认知进化..."
            }
          ]
        ],
        "sort_order": 0,
        "tags": ""
      }
    ],
    "paginated_cards": [
      [
        {
          "type": "number",
          "content": "AI处理结果"
        },
        {
          "type": "body",
          "content": "未来竞争力：联机时代的独立思考与认知进化..."
        }
      ]
    ]
  }
}
```

## 数据流程

1. **用户请求创建卡册** → 发送文本和模板ID
2. **AI处理文本** → 使用千问模型处理文本
3. **分页处理** → 使用分页引擎将文本分割成多个卡片
4. **创建书籍记录** → 在数据库中创建BookM记录
5. **创建卡片记录** → 将分页后的JSON数据存储到CardM.ProcessedText字段
6. **更新书籍信息** → 更新书籍的卡片数量
7. **获取完整书籍信息** → 通过GetByID获取包含Cards关联数据的完整书籍信息
8. **返回BookM** → 返回包含cards数组的完整BookM结构

## 技术特点

### 1. 自动JSON解析

卡片模型的自定义 `MarshalJSON` 方法会自动将存储在 `ProcessedText` 字段中的JSON字符串解析为对象，这样前端可以直接使用解析后的数据。

### 2. 关联数据预加载

通过GORM的 `Preload("Cards")` 方法，确保在获取书籍信息时同时加载关联的卡片数据。

### 3. 数据结构清晰

返回的 `BookM` 结构包含：
- 书籍基本信息（ID、标题、模板ID等）
- 卡片数量（card_count）
- 卡片数组（cards），每个卡片包含解析后的分页数据

### 4. 前端友好

前端可以直接使用返回的数据：
- `data.card_count` 知道总共有多少张卡片
- `data.cards` 数组包含所有卡片信息
- 每个卡片的 `processed_text` 字段包含解析后的分页数据

## 测试方法

1. 创建卡册：
```bash
curl -X POST http://localhost:9091/v1/books \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "text": "你的文本内容",
    "template_id": "1"
  }'
```

2. 获取书籍详情：
```bash
curl -X GET http://localhost:9091/v1/books/{book_id} \
  -H "Authorization: Bearer YOUR_TOKEN"
```

## 优势

1. **数据结构完整**：返回的BookM包含所有必要信息
2. **前端友好**：cards数组便于前端渲染
3. **数据一致性**：通过关联查询确保数据一致性
4. **自动解析**：JSON数据自动解析为对象
5. **向后兼容**：不影响现有API的使用 