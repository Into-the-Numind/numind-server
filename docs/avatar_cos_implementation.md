# 用户头像COS上传和获取功能实现总结

## 🎯 功能概述

成功实现了用户头像的COS（腾讯云对象存储）上传和获取功能，与现有的Card图片COS处理逻辑保持一致。

## 📊 实现对比

### Card图片COS处理流程（现有）
```
1. 图片生成 → 保存到本地
2. COS上传 → card/{cardID}/card_{cardID}.webp
3. 获取时优先返回COS签名URL
4. 失败时回退到本地路径
```

### 用户头像COS处理流程（新增）
```
1. 文件上传 → 保存到本地
2. COS上传 → avatars/{userID}/avatar_{timestamp}.{ext}
3. 获取时优先返回COS签名URL
4. 失败时回退到本地路径
```

## 🔧 技术实现

### 1. 上传逻辑增强

**文件**: `internal/numind/controller/v1/user/update.go`

```go
// 上传到COS（如果启用）
if util.IsCOSEnabled() {
    // 读取上传的文件
    if imageData, err := os.ReadFile(filePath); err == nil {
        // 构建COS对象键：avatars/{user_id}/avatar_{timestamp}.{ext}
        objectKey := fmt.Sprintf("avatars/%d/%s", user.ID, fileName)
        
        // 上传到COS
        cosURL, uploadErr := util.UploadBytesToCOS(c, objectKey, file.Header.Get("Content-Type"), imageData)
        if uploadErr != nil {
            log.C(c).Warnw("上传头像到COS失败", "user_id", user.ID, "error", uploadErr.Error())
        } else if cosURL != "" {
            log.C(c).Infow("✅ 用户头像已上传到COS", "user_id", user.ID, "cos_url", cosURL)
            
            // 生成签名URL（可选，如果需要的话）
            if signedURL, err := util.GenerateSignedURL(c, objectKey, 600); err == nil && signedURL != "" {
                log.C(c).Infow("头像COS签名URL生成成功", "user_id", user.ID, "signed_url", signedURL)
            }
        }
    }
}
```

### 2. COS获取函数

**文件**: `internal/pkg/util/image_path.go`

```go
// GetAvatarWithCOS 获取用户头像URL，优先返回COS链接
func GetAvatarWithCOS(ctx context.Context, userID uint, localPath string) string {
    // 如果本地路径为空，直接返回空
    if localPath == "" {
        return ""
    }
    
    // 检查COS是否启用
    if !IsCOSEnabled() {
        // COS未启用，返回本地路径
        return GetDisplayURL(localPath)
    }
    
    // 从本地路径中提取文件名
    fileName := filepath.Base(localPath)
    if fileName == "" {
        return GetDisplayURL(localPath)
    }
    
    // 构建COS对象键：avatars/{user_id}/{filename}
    objectKey := fmt.Sprintf("avatars/%d/%s", userID, fileName)
    
    // 先检查文件是否存在于COS
    if !CheckObjectExists(ctx, objectKey) {
        // 文件在COS中不存在，返回本地路径
        return GetDisplayURL(localPath)
    }
    
    // 文件存在，尝试生成COS签名URL（10分钟有效期）
    signedURL, err := GenerateSignedURL(ctx, objectKey, 600)
    if err == nil && signedURL != "" {
        // 成功获取COS链接，返回COS URL
        return signedURL
    }
    
    // COS获取失败，返回本地路径
    return GetDisplayURL(localPath)
}
```

### 3. 显示逻辑更新

更新了所有用户信息获取接口，优先使用COS链接：

- `internal/numind/controller/v1/user/get.go` - GetCurrentUser
- `internal/numind/controller/v1/user/auth.go` - GetProfile  
- `internal/numind/controller/v1/user/wechat.go` - WechatLogin

```go
// 转换头像URL用于展示（优先使用COS链接）
if userWithStats.AvatarURL != "" {
    userWithStats.AvatarURL = util.GetAvatarWithCOS(c, userWithStats.ID, userWithStats.AvatarURL)
}
```

## 📁 文件存储结构

### 本地存储
```
res/upload/avatars/
├── 1/
│   ├── avatar_1640995200.jpg
│   └── avatar_1640995300.png
├── 2/
│   └── avatar_1640995400.gif
└── ...
```

### COS存储
```
avatars/
├── 1/
│   ├── avatar_1640995200.jpg
│   └── avatar_1640995300.png
├── 2/
│   └── avatar_1640995400.gif
└── ...
```

## 🔄 处理流程

### 上传流程
1. **文件验证**: 检查文件大小（≤2MB）和格式（JPEG/PNG/GIF）
2. **本地保存**: 保存到 `res/upload/avatars/{userID}/avatar_{timestamp}.{ext}`
3. **COS上传**: 如果COS启用，上传到 `avatars/{userID}/avatar_{timestamp}.{ext}`
4. **数据库更新**: 保存本地路径到用户记录
5. **日志记录**: 记录上传结果和COS状态

### 获取流程
1. **COS检查**: 检查COS是否启用
2. **文件存在性**: 检查COS中是否存在文件
3. **签名URL**: 生成10分钟有效期的签名URL
4. **回退机制**: COS失败时返回本地路径
5. **URL处理**: 去掉/opt前缀用于展示

## ⚙️ 配置要求

### COS配置
```yaml
cos:
  enabled: true
  secret_id: "your_secret_id"
  secret_key: "your_secret_key"
  bucket: "your_bucket_name"
  region: "your_region"
```

### 文件路径配置
```yaml
resource:
  image_path: "/path/to/upload/directory"
```

## 🧪 测试验证

### 测试脚本
创建了 `scripts/test-avatar-cos.sh` 测试脚本，包含：
- 头像上传测试
- 用户信息获取测试
- COS配置检查
- 功能验证

### 手动测试步骤
1. **启动服务**: 确保COS配置正确
2. **上传头像**: `POST /api/v1/users/avatar`
3. **获取用户信息**: `GET /api/v1/users/profile`
4. **验证COS链接**: 检查返回的头像URL是否为COS签名URL

## 📈 性能优化

### 优势
- **CDN加速**: COS提供全球CDN加速
- **高可用性**: 99.9%的可用性保证
- **自动扩展**: 无需担心存储容量限制
- **安全性**: 签名URL提供临时访问权限

### 回退机制
- **本地备份**: 始终保存本地副本
- **优雅降级**: COS失败时自动使用本地文件
- **错误处理**: 完善的错误日志和监控

## 🔒 安全考虑

### 文件验证
- **大小限制**: 最大2MB
- **格式检查**: 仅允许图片格式
- **内容验证**: 检查Content-Type头

### 访问控制
- **签名URL**: 10分钟有效期
- **权限控制**: 基于用户ID的路径隔离
- **临时访问**: 避免永久公开链接

## 📝 API接口

### 上传头像
```
POST /api/v1/users/avatar
Content-Type: multipart/form-data
Authorization: Bearer <jwt_token>

Form Data:
- avatar: 头像文件
```

### 获取用户信息
```
GET /api/v1/users/profile
Authorization: Bearer <jwt_token>
```

## 🎉 总结

✅ **功能完整**: 用户头像COS上传和获取功能已完全实现
✅ **逻辑一致**: 与Card图片COS处理逻辑保持一致
✅ **向后兼容**: 支持本地存储回退机制
✅ **性能优化**: 优先使用COS CDN加速
✅ **安全可靠**: 完善的验证和错误处理
✅ **易于维护**: 清晰的代码结构和日志记录

用户头像现在可以享受与Card图片相同的COS存储优势，提供更好的性能和用户体验！
