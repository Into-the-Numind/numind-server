# 微信小程序图片处理数据库设计

## 概述

本数据库设计支持微信小程序的图片处理功能，实现用户上传图片 → AI处理成卡片 → 多个卡片组成卡册的完整流程。

## 核心流程

1. **用户上传图片** → 存储到 `images` 表
2. **AI处理图片** → 创建处理任务到 `processing_tasks` 表
3. **生成卡片** → 将处理结果存储到 `cards` 表
4. **组织卡册** → 多个卡片组成 `books` 表

## 数据表结构

### 1. users (用户表)
存储微信小程序用户信息，支持普通用户和管理员两种角色。

**主要字段：**
- `openid`: 微信OpenID，唯一标识
- `phone`: 手机号
- `nickname`: 昵称
- `avatar_url`: 头像URL
- `is_pro`: 是否为付费用户
- `book_num`: 创建的卡册数量

### 2. images (图片表)
存储用户上传的原始图片信息。

**主要字段：**
- `user_id`: 上传用户ID
- `original_url`: 原始图片URL
- `thumb_url`: 缩略图URL
- `file_name`: 文件名
- `file_size`: 文件大小
- `width/height`: 图片尺寸
- `status`: 处理状态 (uploaded, processing, processed, failed)

### 3. books (卡册表)
存储用户创建的卡册信息。

**主要字段：**
- `user_id`: 创建用户ID
- `title`: 卡册标题
- `description`: 卡册描述
- `cover_url`: 封面图片URL
- `category`: 分类
- `status`: 状态 (draft, published, archived)
- `is_public`: 是否公开
- `card_count`: 卡片数量 (通过触发器自动更新)

### 4. cards (卡片表)
存储AI处理后的卡片内容。

**主要字段：**
- `user_id`: 创建用户ID
- `book_id`: 所属卡册ID
- `image_id`: 原始图片ID
- `title`: 卡片标题
- `content`: 卡片内容
- `ocr_text`: OCR识别的原始文本
- `processed_text`: AI处理后的文本
- `card_type`: 卡片类型 (text, qa, summary等)
- `status`: 处理状态 (processing, completed, failed)
- `sort_order`: 在卡册中的排序

### 5. processing_tasks (处理任务表)
跟踪AI处理任务的进度和状态。

**主要字段：**
- `user_id`: 用户ID
- `image_id`: 图片ID
- `book_id`: 卡册ID
- `task_type`: 任务类型 (ocr, ai_process, card_generate)
- `status`: 任务状态 (pending, processing, completed, failed)
- `progress`: 进度百分比 (0-100)
- `result`: 处理结果 (JSON格式)
- `error_msg`: 错误信息

### 6. card_rel_image (卡片图片关联表)
支持一张卡片对应多张图片的场景。

**主要字段：**
- `card_id`: 卡片ID
- `url`: 图片URL
- `ocr_text`: 该图片的OCR文本

## 关联关系

```
users (1) ←→ (N) images
users (1) ←→ (N) books
users (1) ←→ (N) processing_tasks

images (1) ←→ (N) cards
images (1) ←→ (N) processing_tasks

books (1) ←→ (N) cards
books (1) ←→ (N) processing_tasks

cards (1) ←→ (N) card_rel_image
```

## 业务流程示例

### 1. 用户上传图片
```sql
-- 1. 插入图片记录
INSERT INTO images (user_id, original_url, file_name, file_size, status)
VALUES (1, 'https://example.com/image.jpg', 'image.jpg', 1024000, 'uploaded');

-- 2. 创建OCR处理任务
INSERT INTO processing_tasks (user_id, image_id, task_type, status)
VALUES (1, LAST_INSERT_ID(), 'ocr', 'pending');
```

### 2. AI处理图片
```sql
-- 1. 更新任务状态为处理中
UPDATE processing_tasks 
SET status = 'processing', started_at = NOW(), progress = 0
WHERE id = 1;

-- 2. 更新进度
UPDATE processing_tasks 
SET progress = 50
WHERE id = 1;

-- 3. 完成处理，更新结果
UPDATE processing_tasks 
SET status = 'completed', progress = 100, completed_at = NOW(),
    result = '{"ocr_text": "识别的文本", "processed_text": "AI处理后的文本"}'
WHERE id = 1;
```

### 3. 生成卡片
```sql
-- 1. 创建卡片
INSERT INTO cards (user_id, image_id, title, content, ocr_text, processed_text, status)
VALUES (1, 1, '卡片标题', '卡片内容', 'OCR文本', 'AI处理文本', 'completed');

-- 2. 更新图片状态
UPDATE images SET status = 'processed' WHERE id = 1;
```

### 4. 创建卡册
```sql
-- 1. 创建卡册
INSERT INTO books (user_id, title, description, status)
VALUES (1, '我的卡册', '卡册描述', 'draft');

-- 2. 将卡片添加到卡册
UPDATE cards SET book_id = LAST_INSERT_ID() WHERE id = 1;
-- 触发器会自动更新books表的card_count字段
```

## 索引设计

### 主要索引
- `users.openid`: 微信登录查询
- `images.user_id`: 用户图片列表
- `images.status`: 按状态查询图片
- `cards.book_id`: 卡册内卡片列表
- `cards.status`: 按状态查询卡片
- `processing_tasks.status`: 查询待处理任务

### 复合索引
- `(user_id, created_at)`: 用户内容按时间排序
- `(book_id, sort_order)`: 卡册内卡片排序

## 性能优化

1. **分页查询**: 使用 `LIMIT` 和 `OFFSET` 进行分页
2. **状态缓存**: 对频繁查询的状态字段建立缓存
3. **异步处理**: 使用消息队列处理AI任务
4. **图片CDN**: 使用CDN加速图片访问
5. **数据库分区**: 按时间对大表进行分区

## 扩展性考虑

1. **多租户**: 可以添加 `tenant_id` 字段支持多租户
2. **版本控制**: 可以添加 `version` 字段支持内容版本管理
3. **软删除**: 可以添加 `deleted_at` 字段支持软删除
4. **审计日志**: 可以创建独立的审计表记录操作日志 