# 创建笔记API使用指南

## 快速开始

### 1. 用户认证
所有API都需要JWT Token认证：

```bash
# 登录获取Token
curl -X POST "https://api.numind.com/auth/login" \
  -H "Content-Type: application/json" \
  -d '{
    "code": "微信授权码"
  }'
```

### 2. 检查创建权限
在创建笔记前，建议先检查用户权限：

```bash
curl -X GET "https://api.numind.com/membership/check-create-permission" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN"
```

响应示例：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "can_create": true,
    "reason": "订阅会员",
    "membership_type": "subscription",
    "is_pro": true,
    "package_count": 0,
    "book_all_num": 5,
    "membership_expires": "2024-12-31T23:59:59Z"
  }
}
```

### 3. 获取可用模板
获取用户可以使用的模板列表：

```bash
curl -X GET "https://api.numind.com/templates" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN"
```

响应示例：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "total_count": 3,
    "templates": [
      {
        "id": 1,
        "name": "简约风格",
        "file": "<html>...</html>",
        "preview": "https://cos.example.com/template1_preview.webp",
        "is_member_only": false,
        "created_at": "2024-01-01 12:00:00",
        "updated_at": "2024-01-01 12:00:00"
      }
    ]
  }
}
```

### 4. 创建笔记
使用选定的模板创建笔记：

```bash
curl -X POST "https://api.numind.com/books" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "text": "这是用户输入的原始文本内容，可以是任何形式的笔记、摘抄、想法等。",
    "template_id": "1"
  }'
```

响应示例：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "id": 123,
    "user_id": 456,
    "title": "AI生成卡册",
    "template_id": "1",
    "status": "creating",
    "card_count": 0,
    "created_at": "2024-01-01 12:00:00",
    "updated_at": "2024-01-01 12:00:00"
  }
}
```

### 5. 查询处理状态
创建后可以查询处理状态：

```bash
curl -X GET "https://api.numind.com/books/123" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN"
```

状态说明：
- `creating`: 创建中
- `ai`: AI处理中
- `success`: 处理完成
- `failed`: 处理失败

## 完整示例

### JavaScript/TypeScript示例

```typescript
class NumindAPI {
  private baseURL = 'https://api.numind.com';
  private token: string;

  constructor(token: string) {
    this.token = token;
  }

  private async request(endpoint: string, options: RequestInit = {}) {
    const response = await fetch(`${this.baseURL}${endpoint}`, {
      ...options,
      headers: {
        'Authorization': `Bearer ${this.token}`,
        'Content-Type': 'application/json',
        ...options.headers,
      },
    });

    if (!response.ok) {
      throw new Error(`API request failed: ${response.statusText}`);
    }

    return response.json();
  }

  // 检查创建权限
  async checkCreatePermission() {
    return this.request('/membership/check-create-permission');
  }

  // 获取模板列表
  async getTemplates() {
    return this.request('/templates');
  }

  // 创建笔记
  async createNote(text: string, templateId: string) {
    return this.request('/books', {
      method: 'POST',
      body: JSON.stringify({
        text,
        template_id: templateId,
      }),
    });
  }

  // 查询笔记状态
  async getNoteStatus(bookId: number) {
    return this.request(`/books/${bookId}`);
  }
}

// 使用示例
async function createNoteExample() {
  const api = new NumindAPI('your_jwt_token');

  try {
    // 1. 检查权限
    const permission = await api.checkCreatePermission();
    if (!permission.data.can_create) {
      console.error('没有创建权限:', permission.data.reason);
      return;
    }

    // 2. 获取模板
    const templates = await api.getTemplates();
    const selectedTemplate = templates.data.templates[0];

    // 3. 创建笔记
    const note = await api.createNote(
      '这是我的笔记内容...',
      selectedTemplate.id.toString()
    );

    console.log('笔记创建成功:', note.data);

    // 4. 轮询查询状态
    const bookId = note.data.id;
    const checkStatus = async () => {
      const status = await api.getNoteStatus(bookId);
      console.log('当前状态:', status.data.status);

      if (status.data.status === 'success') {
        console.log('处理完成!');
      } else if (status.data.status === 'failed') {
        console.error('处理失败');
      } else {
        // 继续轮询
        setTimeout(checkStatus, 2000);
      }
    };

    checkStatus();

  } catch (error) {
    console.error('创建笔记失败:', error);
  }
}
```

### Python示例

```python
import requests
import time
import json

class NumindAPI:
    def __init__(self, token: str):
        self.base_url = 'https://api.numind.com'
        self.token = token
        self.headers = {
            'Authorization': f'Bearer {token}',
            'Content-Type': 'application/json'
        }

    def request(self, endpoint: str, method: str = 'GET', data: dict = None):
        url = f"{self.base_url}{endpoint}"
        
        if method == 'POST':
            response = requests.post(url, headers=self.headers, json=data)
        else:
            response = requests.get(url, headers=self.headers)
        
        response.raise_for_status()
        return response.json()

    def check_create_permission(self):
        return self.request('/membership/check-create-permission')

    def get_templates(self):
        return self.request('/templates')

    def create_note(self, text: str, template_id: str):
        return self.request('/books', 'POST', {
            'text': text,
            'template_id': template_id
        })

    def get_note_status(self, book_id: int):
        return self.request(f'/books/{book_id}')

# 使用示例
def create_note_example():
    api = NumindAPI('your_jwt_token')

    try:
        # 1. 检查权限
        permission = api.check_create_permission()
        if not permission['data']['can_create']:
            print(f"没有创建权限: {permission['data']['reason']}")
            return

        # 2. 获取模板
        templates = api.get_templates()
        selected_template = templates['data']['templates'][0]

        # 3. 创建笔记
        note = api.create_note(
            '这是我的笔记内容...',
            str(selected_template['id'])
        )

        print(f"笔记创建成功: {note['data']}")

        # 4. 轮询查询状态
        book_id = note['data']['id']
        while True:
            status = api.get_note_status(book_id)
            print(f"当前状态: {status['data']['status']}")

            if status['data']['status'] == 'success':
                print("处理完成!")
                break
            elif status['data']['status'] == 'failed':
                print("处理失败")
                break
            else:
                time.sleep(2)

    except Exception as error:
        print(f"创建笔记失败: {error}")

if __name__ == "__main__":
    create_note_example()
```

## 错误处理

### 常见错误码

| 错误码 | 说明 | 解决方案 |
|--------|------|----------|
| 401 | 未授权 | 检查JWT Token是否有效 |
| 403 | 权限不足 | 检查用户会员状态 |
| 400 | 请求参数错误 | 检查请求参数格式 |
| 500 | 服务器内部错误 | 联系技术支持 |

### 错误响应格式

```json
{
  "code": 1,
  "message": "错误描述",
  "data": null
}
```

## 最佳实践

### 1. 权限检查
- 在创建笔记前始终检查用户权限
- 根据权限结果调整UI显示
- 处理权限不足的情况

### 2. 状态轮询
- 创建笔记后定期查询状态
- 设置合理的轮询间隔（建议2-5秒）
- 处理超时情况

### 3. 错误处理
- 实现完整的错误处理逻辑
- 提供用户友好的错误提示
- 记录错误日志用于调试

### 4. 用户体验
- 显示处理进度
- 提供取消功能
- 支持重试机制

## 限制说明

### 免费用户
- 每月最多创建3个笔记
- 无法使用会员专用模板

### 订阅会员
- 无限制创建笔记
- 可使用所有模板
- 优先处理

### 资源包用户
- 按次数计费
- 可使用所有模板
- 次数用完后需要购买新的资源包
