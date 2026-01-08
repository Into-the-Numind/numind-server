# Web端用户名密码登录功能说明

## 功能概述

新增了Web端用户名密码登录功能，支持用户通过用户名和密码进行身份验证。

## 实现细节

### 1. 数据库字段
- User表已包含 `username` 和 `password` 字段
- `password` 字段明文存储（根据需求）
- 字段会通过AutoMigrate自动创建/更新

### 2. API接口

**接口地址**: `POST /v1/web/login`

**请求参数**:
```json
{
  "username": "test_user",
  "password": "admin123456"
}
```

**响应示例**:
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "token_type": "Bearer",
    "user": {
      "id": 1,
      "username": "test_user",
      "nickname": "测试用户",
      // ... 其他用户信息
    }
  }
}
```

### 3. Token使用方式

登录成功后，在后续请求的Header中携带token：
```
Authorization: Bearer <access_token>
```

## 测试步骤

### 1. 准备测试用户

在数据库中创建或更新用户，设置密码为 `admin123456`：

```sql
-- 方式1: 更新现有用户
UPDATE user SET password = 'admin123456' WHERE username = 'test_user';

-- 方式2: 创建新用户（如果不存在）
INSERT INTO user (username, password, nickname, created_at, updated_at) 
VALUES ('test_user', 'admin123456', '测试用户', NOW(), NOW());
```

### 2. 测试登录

使用curl或Postman测试：

```bash
curl -X POST http://localhost:8080/v1/web/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "test_user",
    "password": "admin123456"
  }'
```

### 3. 使用Token访问受保护接口

```bash
curl -X GET http://localhost:8080/v1/books \
  -H "Authorization: Bearer <your_access_token>"
```

## 实现文件清单

1. **API定义**: `pkg/api/numind/v1/user.go`
   - 添加了 `WebLoginRequest` 和 `WebLoginResponse` 结构体

2. **业务逻辑**: `internal/numind/biz/user/user.go`
   - 添加了 `WebLogin` 方法到 `UserBiz` 接口
   - 实现了 `WebLogin` 方法，包含用户验证、密码校验和token生成

3. **控制器**: `internal/numind/controller/v1/user/wechat.go`
   - 添加了 `WebLogin` handler方法

4. **路由注册**: `internal/numind/router.go`
   - 注册了 `/v1/web/login` 路由（无需鉴权）

## 安全建议

⚠️ **重要提示**: 当前实现使用明文存储密码，仅用于开发测试环境。

生产环境建议：
1. 使用bcrypt等加密算法对密码进行哈希存储
2. 实施密码强度验证
3. 添加登录失败次数限制
4. 添加验证码防止暴力破解
5. 记录登录审计日志

## 与现有功能的兼容性

- ✅ 与微信登录功能完全独立
- ✅ 使用相同的JWT token机制
- ✅ 兼容现有的认证中间件
- ✅ 支持相同的用户权限系统
