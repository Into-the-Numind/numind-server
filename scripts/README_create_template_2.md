# 创建Template 2的SOP

## 说明

此目录包含创建template id为2的SOP模板的脚本。

## 方法1: 使用Go脚本（推荐）

### 前提条件
- Go环境已安装
- MySQL数据库正在运行
- 数据库配置正确（在config_local.yaml或其他配置文件中）

### 运行方式

```bash
cd /Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server
go run scripts/create_template_2.go
```

### 脚本功能
1. 自动加载配置文件（尝试config_local, config_dev, config_qa, config_prod）
2. 连接数据库
3. 查询template 1的配置和所有节点
4. 创建template 2（名称为"感悟型朋友圈创作"，prompt为空）
5. 创建4个节点：
   - 节点1 (sort=0): "拆解产品"
   - 节点2 (sort=1): "拆解爆款朋友圈"
   - 节点3 (sort=2): "拆解语言风格"
   - 节点4 (sort=3): "仿写朋友圈"
6. 验证创建结果

## 方法2: 使用SQL脚本

如果Go脚本无法连接数据库，可以使用SQL脚本：

### 前提条件
- MySQL客户端可用
- 能够连接到数据库

### 运行方式

```bash
# 方式1: 使用mysql客户端
mysql -h <host> -u <username> -p <database> < scripts/create_template_2.sql

# 方式2: 如果使用Docker
docker exec -i <mysql_container> mysql -u <username> -p<password> <database> < scripts/create_template_2.sql
```

### SQL脚本说明
- 自动从template 1复制配置
- 创建template 2（名称为"感悟型朋友圈创作"，prompt为空）
- 创建4个节点，配置与template 1相同，但名称不同
- 包含验证查询

## 方法3: 使用管理API

如果服务器正在运行，可以使用管理API：

### 步骤1: 获取admin token

```bash
curl -X POST http://localhost:9099/v1/admin/login \
  -H "Content-Type: application/json" \
  -d '{
    "username": "admin",
    "password": "admin123456"
  }'
```

### 步骤2: 创建template 2

```bash
curl -X POST http://localhost:9099/v1/admin/sop/templates \
  -H "Authorization: Bearer <admin_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "感悟型朋友圈创作",
    "description": "",
    "prompt": ""
  }'
```

### 步骤3: 查询template 1的节点

```bash
curl -X GET http://localhost:9099/v1/admin/sop/templates/1/nodes \
  -H "Authorization: Bearer <admin_token>"
```

### 步骤4: 创建4个节点

根据template 1的节点配置，创建4个新节点（template_id=2，名称不同）。

## 验证

创建完成后，可以验证：

```bash
# 查询template 2
curl -X GET http://localhost:9099/v1/admin/sop/templates/2 \
  -H "Authorization: Bearer <admin_token>"

# 查询template 2的所有节点
curl -X GET http://localhost:9099/v1/admin/sop/templates/2/nodes \
  -H "Authorization: Bearer <admin_token>"
```

## 注意事项

1. Template 2的prompt字段必须为空（没有系统级提示词）
2. 节点的配置（base_url, model_name, api_key, timeout_seconds, prompt等）与template 1完全相同
3. 节点的名称与template 1不同：
   - 节点1: "拆解产品"
   - 节点2: "拆解爆款朋友圈"
   - 节点3: "拆解语言风格"
   - 节点4: "仿写朋友圈"
4. 节点的sort值必须保持与template 1相同的顺序（0, 1, 2, 3）












