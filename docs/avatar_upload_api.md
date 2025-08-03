# 用户头像上传API文档

## 概述

用户头像上传功能允许用户上传个人头像图片，系统会将图片保存到服务器的指定目录中，并更新用户信息中的头像URL。

## API接口

### 上传头像

**接口地址**: `POST /v1/users/avatar`

**请求头**:
```
Authorization: Bearer <jwt_token>
Content-Type: multipart/form-data
```

**请求参数**:
- `avatar`: 头像文件（multipart/form-data）

**文件要求**:
- 支持格式: JPEG, JPG, PNG, GIF
- 文件大小: 不超过2MB
- 文件字段名: `avatar`

**响应示例**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "avatar_url": "/static/avatars/123/avatar_1640995200.jpg"
  }
}
```

**错误响应**:
```json
{
  "code": 1,
  "message": "头像文件大小不能超过2MB",
  "data": null
}
```

## 文件存储结构

头像文件将保存在以下目录结构中：

```
/opt/numind/image/upload/
└── avatars/
    ├── 1/
    │   ├── avatar_1640995200.jpg
    │   └── avatar_1640995300.png
    ├── 2/
    │   └── avatar_1640995400.gif
    └── ...
```

### 目录说明

- 根目录: `/opt/numind/image/upload/avatars/`
- 用户目录: `{user_id}/`
- 文件命名: `avatar_{timestamp}.{extension}`

## 访问URL

上传后的头像可以通过以下URL访问：

```
http://your-domain.com/static/avatars/{user_id}/{filename}
```

## 配置要求

### 1. 目录权限

确保应用有权限创建和写入目录：

```bash
# 创建目录
sudo mkdir -p /opt/numind/image/upload/avatars

# 设置权限
sudo chown -R numind:numind /opt/numind/image/upload
sudo chmod -R 755 /opt/numind/image/upload
```

### 2. Nginx配置

在Nginx配置中添加静态文件服务：

```nginx
server {
    listen 80;
    server_name your-domain.com;
    
    # 静态文件服务
    location /static/ {
        alias /opt/numind/image/upload/;
        expires 1y;
        add_header Cache-Control "public, immutable";
        
        # 图片压缩
        location ~* \.(jpg|jpeg|png|gif)$ {
            expires 1y;
            add_header Cache-Control "public, immutable";
        }
    }
    
    # API代理
    location /api/ {
        proxy_pass http://numind-server:8000/;
        # ... 其他配置
    }
}
```

### 3. Docker配置

在docker-compose.yml中添加卷挂载：

```yaml
numind-server:
  # ... 其他配置
  volumes:
    - ./uploads:/opt/numind/image/upload

nginx:
  # ... 其他配置
  volumes:
    - ./uploads:/opt/numind/image/upload:ro
```

## 测试

### 使用curl测试

```bash
curl -X POST \
  -H "Authorization: Bearer your-jwt-token" \
  -F "avatar=@avatar.jpg" \
  http://localhost:9091/v1/users/avatar
```

### 使用测试脚本

```bash
./scripts/test-avatar-upload.sh "your-jwt-token" "avatar.jpg"
```

## 安全考虑

1. **文件类型验证**: 只允许图片格式文件
2. **文件大小限制**: 限制为2MB以内
3. **目录权限**: 确保目录权限正确设置
4. **用户隔离**: 每个用户的文件存储在独立目录中

## 错误处理

| 错误代码 | 错误信息 | 原因 |
|---------|---------|------|
| 1 | 用户未登录 | 缺少或无效的JWT token |
| 1 | 请选择要上传的头像文件 | 未提供文件或文件字段名错误 |
| 1 | 头像文件大小不能超过2MB | 文件超过大小限制 |
| 1 | 只支持JPEG、PNG、GIF格式的图片 | 文件格式不支持 |
| 1 | 创建用户头像目录失败 | 目录权限问题 |
| 1 | 保存头像文件失败 | 文件保存失败 |

## 注意事项

1. 每次上传新头像会覆盖旧头像文件
2. 建议在客户端实现图片压缩功能
3. 可以考虑添加图片裁剪功能
4. 建议定期清理未使用的头像文件 