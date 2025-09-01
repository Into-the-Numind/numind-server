# 增强版卡片渲染器集成测试指南

## 测试前准备

### 1. 环境配置
```bash
# 确保Chrome/Chromium已安装
which google-chrome || which chromium-browser

# 设置环境变量启用增强版渲染器
export ENABLE_ENHANCED_RENDERER=true
export ENABLE_RENDER_AND_MEASURE=true  # 降级选项
export ENABLE_TRADITIONAL_RENDERER=true  # 最后备选
```

### 2. 代码检查
```bash
# 检查所有新增文件是否存在
ls internal/numind/biz/book/enhanced_renderer_integration.go
ls internal/numind/biz/card/enhanced_card_renderer.go
ls internal/numind/biz/card/super_long_image_processor.go
ls internal/numind/biz/card/precise_measurement_engine.go
ls internal/numind/biz/card/card_renderer_coordinator.go

# 运行linting检查
go vet ./internal/numind/biz/book/
go vet ./internal/numind/biz/card/
```

## 功能测试用例

### 测试用例1：基本增强版渲染
**目标**：验证增强版渲染器的基本功能

**输入数据**：
```json
{
  "text": "这是一个测试标题。这是测试的正文内容，用来验证增强版渲染器的基本功能。",
  "template_id": "1"
}
```

**期望结果**：
- 第一张卡片：上半部分显示阿里文生图（60%），下半部分显示标题（40%）
- 后续卡片：按样式规则渲染正文内容
- 所有卡片尺寸：1080×1440px

**验证点**：
- [ ] 第一张卡片布局正确
- [ ] 样式符合规范
- [ ] 图片尺寸正确
- [ ] 数据库记录正确

### 测试用例2：复杂结构化内容渲染
**目标**：验证多种type元素的渲染效果

**输入数据**：
```json
{
  "text": "# 主标题\n## 副标题\n这是正文内容。\n> 这是引用内容\n- 列表项1\n- 列表项2\n- 列表项3",
  "template_id": "1"
}
```

**期望结果**：
- subtitle：24px字体，#4A4A4A颜色，灰色分割线
- body：18px字体，1.8倍行距，两端对齐
- quote：#F0F7FF背景，蓝色左边框，斜体
- list：・前缀（#FF6B35），30px缩进

**验证点**：
- [ ] 各type样式正确
- [ ] 颜色和字体符合规范
- [ ] 边距和间距正确
- [ ] 分页算法正确

### 测试用例3：降级机制测试
**目标**：验证错误情况下的降级处理

**测试步骤**：
1. 临时禁用增强版渲染器：`export ENABLE_ENHANCED_RENDERER=false`
2. 提交book创建请求
3. 观察日志输出

**期望结果**：
- 自动降级到渲染-测量方案
- 如果也失败，继续降级到传统渲染器
- 不影响book创建的整体流程

**验证点**：
- [ ] 降级逻辑正确
- [ ] 错误日志详细
- [ ] 最终能成功创建book

### 测试用例4：性能和并发测试
**目标**：验证在负载情况下的表现

**测试步骤**：
1. 并发提交多个book创建请求
2. 监控系统资源使用
3. 检查渲染质量一致性

**期望结果**：
- 并发处理不影响渲染质量
- 内存和CPU使用合理
- Chrome进程正确管理

**验证点**：
- [ ] 并发处理正确
- [ ] 资源使用合理
- [ ] 无内存泄漏

## API测试

### 创建Book API测试
```bash
curl -X POST http://localhost:8080/v1/books \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -d '{
    "text": "这是测试文本，包含多种内容类型。## 副标题\n正文内容测试。\n> 引用内容测试\n- 列表项测试",
    "template_id": "1"
  }'
```

**期望响应**：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "id": 123,
    "title": "AI生成卡册 - 2024-01-01 12:00:00",
    "status": "creating",
    "card_count": 0,
    "created_at": "2024-01-01T12:00:00Z"
  }
}
```

### 检查Book状态
```bash
curl -X GET http://localhost:8080/v1/books/123 \
  -H "Authorization: Bearer YOUR_TOKEN"
```

**期望响应**（处理完成后）：
```json
{
  "code": 0,
  "message": "success",
  "data": {
    "id": 123,
    "title": "提取的实际标题",
    "status": "success",
    "card_count": 3,
    "image_url": "/images/book_123_cover.jpg",
    "cards": [
      {
        "id": 456,
        "sort_order": 0,
        "rendered_image": "/images/card/cover_456.png"
      },
      {
        "id": 457,
        "sort_order": 1,
        "rendered_image": "/images/card/card_457.png"
      }
    ]
  }
}
```

## 日志监控

### 关键日志检查点

1. **增强版渲染器启动**
```
INFO: 尝试使用增强版渲染器 book_id=123
INFO: 选择渲染策略 book_id=123 strategy=0
```

2. **渲染过程**
```
INFO: 开始增强版渲染处理 book_id=123 elements_count=5
INFO: 📏 精确测量完成，共 5 个测量结果
INFO: ✅ 卡片渲染协调完成，耗时: 1500ms, 生成卡片数: 3
```

3. **成功完成**
```
INFO: 增强版渲染器处理完成 book_id=123
INFO: 增强版渲染完成，跳过传统渲染流程 book_id=123
INFO: Async book creation completed book_id=123 duration=5.2
```

### 错误日志检查
```
ERROR: 增强版渲染器处理失败，降级到传统方案 book_id=123 error=...
WARN: 渲染-测量渲染器创建失败，降级到传统渲染器 book_id=123
```

## 文件系统验证

### 图片文件检查
```bash
# 检查卡片图片是否正确生成
ls -la images/upload/card/*/
file images/upload/card/*/card_*.png

# 验证图片尺寸
identify images/upload/card/*/card_*.png
# 期望输出：card_xxx.png PNG 1080x1440 ...
```

### 临时文件清理
```bash
# 检查是否有残留的调试文件
ls -la debug_*.html measure_*.html render_*.html
# 这些文件应该在渲染完成后被清理
```

## 数据库验证

### 检查Book记录
```sql
SELECT id, title, status, card_count, image_url, created_at, updated_at 
FROM books 
WHERE id = 123;
```

### 检查Card记录
```sql
SELECT id, book_id, sort_order, rendered_image, processed_text
FROM cards 
WHERE book_id = 123 
ORDER BY sort_order;
```

**期望结果**：
- sort_order=0：封面卡片
- sort_order≥1：内容卡片，按顺序排列
- rendered_image：所有卡片都有图片URL
- processed_text：JSON格式的元素数据

## 回归测试

### 兼容性验证
1. **关闭增强版渲染器**
```bash
export ENABLE_ENHANCED_RENDERER=false
```

2. **验证传统流程仍然正常**
- 创建book应该成功
- 使用原有的渲染逻辑
- API响应格式不变

3. **重新启用增强版渲染器**
```bash
export ENABLE_ENHANCED_RENDERER=true
```

### 数据完整性检查
- 用户统计数据正确更新
- 卡片数量统计准确
- 图片URL有效可访问

## 故障排除

### 常见问题

1. **Chrome启动失败**
```
ERROR: 启动Chrome失败: exec: "google-chrome": executable file not found
```
**解决方案**：安装Chrome或设置正确的可执行文件路径

2. **内存不足**
```
ERROR: Chrome渲染失败: context deadline exceeded
```
**解决方案**：增加系统内存或调整并发数量

3. **权限问题**
```
ERROR: 创建目录失败: permission denied
```
**解决方案**：检查图片存储目录权限

### 调试模式
```bash
# 启用详细日志
export LOG_LEVEL=debug

# 保留调试文件
export KEEP_DEBUG_FILES=true
```

## 性能基准

### 预期性能指标
- **简单内容（≤5个元素）**：< 2秒
- **中等复杂度（6-20个元素）**：< 5秒  
- **复杂内容（>20个元素）**：< 10秒

### 资源使用限制
- **内存使用**：每个渲染进程 < 500MB
- **CPU使用**：峰值 < 80%
- **磁盘IO**：图片文件大小 < 2MB/张

## 测试报告模板

```markdown
## 增强版卡片渲染器测试报告

**测试时间**：2024-XX-XX
**测试环境**：开发/测试/生产
**测试版本**：vX.X.X

### 功能测试结果
- [ ] 基本增强版渲染：通过/失败
- [ ] 复杂结构化内容：通过/失败
- [ ] 降级机制：通过/失败
- [ ] 性能并发：通过/失败

### API测试结果
- [ ] 创建Book API：通过/失败
- [ ] 状态查询API：通过/失败
- [ ] 响应格式：通过/失败

### 性能指标
- 平均渲染时间：X秒
- 内存使用峰值：XMB
- 并发处理能力：X个/分钟

### 问题记录
1. [问题描述] - [解决方案]
2. [问题描述] - [解决方案]

### 结论
增强版卡片渲染器 [✅通过] / [❌未通过] 所有测试用例。
```
