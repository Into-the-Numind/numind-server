# Template 会员可用字段实现说明

## 概述

为template表添加了`is_member_only`字段，用于区分模板是否仅会员可用。这个字段允许系统根据用户的会员状态来控制模板的访问权限。

## 实现内容

### 1. 数据库模型更新

**文件**: `internal/pkg/model/template.go`

```go
type Template struct {
    gorm.Model
    Name         string `gorm:"size:50;not null;uniqueIndex" json:"name" valid:"required"`
    File         string `gorm:"type:text;not null" json:"file" valid:"required"`
    IsMemberOnly bool   `gorm:"default:false;not null" json:"is_member_only"` // 是否仅会员可用
}
```

### 2. API 结构更新

**文件**: `pkg/api/numind/v1/user.go`

#### 创建模板请求
```go
type CreateTemplateRequest struct {
    Name         string `json:"name" binding:"required" valid:"required,stringlength(1|50)"`
    File         string `json:"file" binding:"required" valid:"required"`
    IsMemberOnly bool   `json:"is_member_only"` // 是否仅会员可用
}
```

#### 更新模板请求
```go
type UpdateTemplateRequest struct {
    Name         *string `json:"name" valid:"stringlength(1|50)"`
    File         *string `json:"file"`
    IsMemberOnly *bool   `json:"is_member_only"` // 是否仅会员可用
}
```

#### 模板响应
```go
type TemplateResponse struct {
    ID           uint   `json:"id"`
    Name         string `json:"name"`
    File         string `json:"file"`
    IsMemberOnly bool   `json:"is_member_only"` // 是否仅会员可用
    CreatedAt    string `json:"created_at"`
    UpdatedAt    string `json:"updated_at"`
}
```

### 3. 控制器更新

**文件**: `internal/numind/controller/v1/template/create.go`
- 在创建模板时处理`IsMemberOnly`字段

**文件**: `internal/numind/controller/v1/template/update.go`
- 在更新模板时处理`IsMemberOnly`字段

### 4. 数据库迁移

**文件**: `docs/template_member_only_migration.sql`

```sql
-- 添加 is_member_only 字段
ALTER TABLE `template` 
ADD COLUMN `is_member_only` tinyint(1) NOT NULL DEFAULT 0 COMMENT '是否仅会员可用' AFTER `file`;

-- 为现有数据设置默认值
UPDATE `template` SET `is_member_only` = 0 WHERE `is_member_only` IS NULL;

-- 添加索引以提高查询性能
CREATE INDEX `idx_template_is_member_only` ON `template` (`is_member_only`);
```

### 5. API 文档更新

**文件**: `docs/template_api.md`
- 更新了所有API示例，包含`is_member_only`字段
- 添加了字段说明和注意事项

## API 使用示例

### 创建会员专用模板

```bash
POST /v1/templates
Content-Type: application/json
Authorization: Bearer <token>

{
  "name": "会员专用模板",
  "file": "模板内容",
  "is_member_only": true
}
```

### 创建普通模板

```bash
POST /v1/templates
Content-Type: application/json
Authorization: Bearer <token>

{
  "name": "普通模板",
  "file": "模板内容",
  "is_member_only": false
}
```

### 更新模板为会员专用

```bash
PUT /v1/templates/1
Content-Type: application/json
Authorization: Bearer <token>

{
  "is_member_only": true
}
```

## 响应示例

```json
{
  "code": 0,
  "message": "OK",
  "data": {
    "id": 1,
    "name": "会员专用模板",
    "file": "模板内容",
    "is_member_only": true,
    "created_at": "2025-01-07T10:00:00Z",
    "updated_at": "2025-01-07T10:00:00Z"
  }
}
```

## 部署步骤

1. **执行数据库迁移**
   ```bash
   mysql -u username -p database_name < docs/template_member_only_migration.sql
   ```

2. **重新编译和部署应用**
   ```bash
   go build -o numind cmd/numind/main.go
   ```

3. **验证功能**
   - 测试创建模板API，包含`is_member_only`字段
   - 测试更新模板API，修改`is_member_only`字段
   - 测试获取模板列表API，确认返回`is_member_only`字段

## 注意事项

1. **向后兼容性**: 现有模板的`is_member_only`字段默认为`false`，保持向后兼容
2. **默认值**: 新创建的模板如果不指定`is_member_only`，默认为`false`
3. **索引优化**: 添加了索引以提高按会员状态查询的性能
4. **API兼容性**: 所有现有API调用仍然有效，新字段为可选参数

## 后续扩展建议

1. **权限控制**: 在业务逻辑中添加基于用户会员状态和模板`is_member_only`字段的访问控制
2. **过滤功能**: 在获取模板列表API中添加按会员状态过滤的参数
3. **统计功能**: 添加统计会员专用模板数量的功能
