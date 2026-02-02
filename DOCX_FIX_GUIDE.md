# DOCX 文件解析问题修复指南

## 问题描述

1. **之前**: DOCX 文件解析为乱码（PK-!2{jsword/...）
2. **现在**: DOCX 文件解析失败（extension: 空）

## 根本原因

数据库中的 `knowledge_document` 表存在**错误数据**：
- `name` 字段存储的不是文件名，而是空值或数字（如 "1", "66"）
- 导致扩展名识别失败，无法选择正确的解析器

## 修复步骤

### 步骤 1: 检查问题数据

```bash
# SSH 登录服务器
ssh root@49.233.219.254

# 连接数据库
mysql -u root -p numind-dev

# 查看问题数据
SELECT
    id,
    name,
    CHAR_LENGTH(name) as name_len,
    file_path,
    status
FROM knowledge_document
WHERE name = ''
   OR name IS NULL
   OR CHAR_LENGTH(name) <= 2
ORDER BY id DESC;
```

### 步骤 2: 清理错误数据

**选项 A - 删除错误记录（推荐）**：

```sql
-- 先备份
CREATE TABLE knowledge_document_backup AS
SELECT * FROM knowledge_document;

-- 删除错误记录
DELETE FROM knowledge_document
WHERE name = ''
   OR name IS NULL
   OR CHAR_LENGTH(name) <= 2;

-- 同时清理关联的 chunks
DELETE FROM knowledge_chunk
WHERE document_id NOT IN (
    SELECT id FROM knowledge_document
);
```

**选项 B - 手动修复（如果数据重要）**：

```sql
-- 查看完整信息
SELECT
    id,
    name,
    file_path,
    status,
    created_at
FROM knowledge_document
WHERE id IN (43, 44);

-- 如果能从 file_path 中提取文件名，手动更新
-- 示例（需要根据实际情况调整）:
UPDATE knowledge_document
SET name = 'document_name.docx'  -- 替换为实际文件名
WHERE id = 43;
```

### 步骤 3: 更新代码

代码已经更新，包含以下改进：

1. **文件名验证**（`salesrag.go`）:
   - 检查文件名不为空
   - 检查文件名长度 > 2
   - 验证包含文件扩展名

2. **URL 参数清理**（`enhanced_parser.go`）:
   - 移除 URL 查询参数（?sign=xxx）
   - 提取纯文件名用于扩展名识别

3. **详细日志**（`pipeline.go`）:
   - 记录解析的文件名
   - 记录解析错误详情

4. **错误处理改进**:
   - default 分支返回明确错误而不是转换乱码

### 步骤 4: 重新部署

```bash
# 拉取最新代码
cd /path/to/numind-server
git pull

# 重新构建镜像
docker-compose build numind-server

# 重启服务
docker-compose down
docker-compose up -d

# 查看日志
docker-compose logs -f numind-server | grep -i "docx\|parsing"
```

### 步骤 5: 测试验证

1. 上传一个新的 DOCX 文件到知识库
2. 查看日志确认使用正确的文件名：
   ```
   DEBUG: Parsing document - ID: XX, Name: 'file.docx', FilePath: 'https://...'
   ```
3. 确认文档状态变为 COMPLETED
4. 测试检索功能

## 预防措施

### 1. 前端验证

确保前端上传时传递正确的参数：

```javascript
const formData = new FormData();
formData.append('file', file);
formData.append('name', file.name);  // ← 确保传递文件名
formData.append('description', description);
```

### 2. API 调用示例

```bash
curl -X POST "http://localhost:9091/v1/sales-rag/ingest" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -F "file=@/path/to/document.docx" \
  -F "name=document.docx" \
  -F "description=测试文档"
```

### 3. 监控日志

定期检查是否有解析失败：

```bash
# 每天检查一次
grep "parsing failed" numind.log | tail -20
```

## 代码改动总结

### 修改的文件

1. `internal/numind/biz/salesrag/service/pipeline.go`
   - 第 134 行: 使用 `doc.Name` 而不是 `doc.FilePath`
   - 添加详细的调试日志

2. `internal/numind/biz/salesrag/adapter/enhanced_parser.go`
   - 添加 URL 查询参数清理逻辑
   - 改进 default 分支的错误处理

3. `internal/numind/biz/salesrag/salesrag.go`
   - 添加文件名验证（空值、长度、扩展名检查）
   - 添加日志记录

### 新增的脚本

1. `scripts/fix_broken_docs.sql` - 数据库修复脚本
2. `scripts/install_python_deps.sh` - Python 依赖安装脚本
3. `DOCX_FIX_GUIDE.md` - 本修复指南

## 常见问题

### Q1: 为什么 SOP 能正常解析 DOCX 而 Sales RAG 不行？

**A**: SOP 使用原始上传文件名（`header.Filename`），而 Sales RAG 之前使用的是 COS URL（包含查询参数），导致扩展名识别失败。

### Q2: Python 依赖是必须的吗？

**A**: 不是必须的。Python 增强解析失败后会降级到 Go 原生 XML 解析，也能提取文本，只是质量稍差（丢失表格）。

### Q3: 如何安装 Python 依赖？

**A**:
```bash
pip3 install PyMuPDF python-docx -i https://pypi.tuna.tsinghua.edu.cn/simple
```

或使用提供的脚本：
```bash
./scripts/install_python_deps.sh
```

### Q4: 如何验证 Python 依赖已安装？

**A**:
```bash
python3 << 'EOF'
try:
    import fitz
    from docx import Document
    print("✅ 依赖已安装")
except ImportError as e:
    print("❌ 依赖缺失:", e)
EOF
```

## 技术细节

### 扩展名识别逻辑

```go
// 原来的代码（有 Bug）
ext := filepath.Ext(doc.FilePath)
// doc.FilePath = "https://cos.com/file.docx?sign=xxx"
// ext = ".docx?sign=xxx"  ← 无法匹配 ".docx"

// 修复后的代码
cleanFilename := doc.Name
if idx := strings.Index(cleanFilename, "?"); idx != -1 {
    cleanFilename = cleanFilename[:idx]
}
ext := filepath.Ext(cleanFilename)
// cleanFilename = "file.docx"
// ext = ".docx"  ← 正确匹配
```

### 解析流程

```
上传文件
  ↓
Controller: Ingest (验证文件名)
  ↓
Biz: Ingest (上传到 COS, 创建记录)
  ↓
Pipeline: process (异步处理)
  ↓
Parser: Parse (doc.Name) ← 使用原始文件名
  ├─ 清理 URL 参数
  ├─ 识别扩展名
  └─ 选择解析器:
      ├─ .docx → Python 增强解析 → Go XML 降级
      ├─ .pdf  → Python PyMuPDF → go-fitz 降级
      └─ .txt/.md → 直接读取
```

## 联系支持

如果问题仍未解决，请提供：
1. 完整的错误日志
2. 数据库查询结果
3. 上传的文件类型和大小
