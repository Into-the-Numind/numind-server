# Numind Server  

## 功能特点

- 微信小程序登录认证
- 提取微信公众号文章内容
- 文章收藏功能
- 用户管理
- RESTful API接口

## 技术栈

- Gin (Web框架)
- SQLAlchemy (ORM)
- MySQL (数据库)
- Pydantic (数据验证)
- JWT (认证)
- BeautifulSoup (HTML解析)

## 数据库设计

系统使用MySQL数据库，包含以下表：

- **users表**: 存储用户信息
    - id: 用户ID，主键
    - openid: 微信用户唯一标识
    - nickname: 用户昵称
    - avatar_url: 用户头像URL
    - created_at: 创建时间
    - is_active: 是否活跃

- **articles表**: 存储微信文章
    - id: 文章ID，主键
    - url: 文章URL地址
    - title: 文章标题
    - account_name: 公众号名称
    - publish_time: 发布时间
    - content: 文章内容（JSON格式）
    - raw_html: 原始HTML内容
    - created_at: 创建时间

- **favorites表**: 存储用户收藏
    - id: 收藏ID，主键
    - user_id: 用户ID（软外键关联users表）
    - article_id: 文章ID（软外键关联articles表）
    - created_at: 创建时间

> 注意：本系统使用软外键设计，没有设置强制的外键约束，但在应用层面保持数据完整性。

## 快速开始

### 1. 安装依赖

```bash
pip install -r requirements.txt
```

### 2. 配置环境变量

复制`.env.example`到`.env`并根据你的环境进行配置：

```
# 数据库配置
DB_USER=root
DB_PASSWORD=your_password
DB_HOST=localhost
DB_PORT=3306
DB_NAME=wechat_articles

# JWT配置
SECRET_KEY=your_secret_key
ALGORITHM=HS256
ACCESS_TOKEN_EXPIRE_MINUTES=30

# 微信小程序配置
WECHAT_APP_ID=your_app_id
WECHAT_APP_SECRET=your_app_secret
```

### 3. 创建数据库

```bash
mysql -u root -p
```

```sql
CREATE DATABASE numind CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

### 4. 初始化数据库表

```bash
python create_db.py
```

### 5. 启动服务器

```bash
python run.py
```

或者直接使用uvicorn:

```bash
uvicorn main:app --reload
```

## API文档

启动服务器后，访问以下URL查看自动生成的API文档：

- Swagger UI: http://localhost:8000/docs
- ReDoc: http://localhost:8000/redoc

## API端点

### 认证

- `POST /auth/wechat-login` - 微信小程序登录
- `PUT /auth/update-profile` - 更新用户资料

### 文章

- `POST /articles/fetch` - 获取微信公众号文章
- `GET /articles/` - 获取文章列表
- `GET /articles/{article_id}` - 获取文章详情

### 收藏

- `POST /articles/favorites` - 添加收藏
- `GET /articles/favorites/` - 获取用户收藏列表
- `DELETE /articles/favorites/{favorite_id}` - 取消收藏 
