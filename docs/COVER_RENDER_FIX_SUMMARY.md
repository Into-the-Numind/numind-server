# 封面卡片渲染修复总结

## 问题描述

用户反馈："看生成的html，没有包含 book路径下的图片，仔细排查，解决封面渲染的问题"

### 发现的问题

1. **CSS格式化错误**：HTML中出现 `%!s(MISSING)` 和 `%!d(MISSING)` 等格式化错误
2. **占位符不匹配**：`replaceCoverImagePlaceholder` 中的占位符结构与实际生成的HTML不匹配
3. **图片路径问题**：封面图片路径构建不正确，没有使用 `file://` 协议
4. **缺少调试信息**：无法知道占位符替换是否成功

## 解决方案

### 1. 修复CSS格式化错误

**文件**: `internal/numind/biz/markdown/html_converter.go`

**问题**: CSS中的百分比符号 `%` 在 `fmt.Sprintf` 中需要转义为 `%%`

**修复内容**:
```go
// 修复前
.cover-card-container {
    width: 100%;
    height: 100%;
    position: relative;
}

// 修复后
.cover-card-container {
    width: 100%%;
    height: 100%%;
    position: relative;
}
```

**修复的CSS属性**:
- `width: 100%` → `width: 100%%`
- `height: 100%` → `height: 100%%`
- `flex: 0 0 65%` → `flex: 0 0 65%%`
- `flex: 0 0 35%` → `flex: 0 0 35%%`
- `max-width: 100%` → `max-width: 100%%`
- `max-height: 100%` → `max-height: 100%%`
- `width: 80%` → `width: 80%%`
- `height: 80%` → `height: 80%%`

### 2. 修复占位符匹配问题

**文件**: `internal/numind/biz/markdown/lightweight_renderer.go`

**问题**: 占位符的HTML结构与实际生成的HTML不匹配

**修复内容**:
```go
// 修复前
placeholderHTML := `<div class="cover-image-placeholder">
            <div class="placeholder-icon">🖼️</div>
            <div class="placeholder-text">封面图片</div>
        </div>`

// 修复后
placeholderHTML := `<div class="cover-image-placeholder">
                <div class="placeholder-icon">🖼️</div>
                <div class="placeholder-text">封面图片</div>
            </div>`
```

**关键修复**: 调整缩进，确保占位符HTML结构与 `generateCoverCardHTML` 生成的完全一致

### 3. 修复图片路径问题

**文件**: `internal/numind/biz/markdown/lightweight_renderer.go`

**问题**: 封面图片路径没有使用 `file://` 协议

**修复内容**:
```go
// 修复前
coverImagePath := fmt.Sprintf("/res/upload/book/%d/book_%d.webp", bookID, bookID)

// 修复后
coverImagePath := fmt.Sprintf("file://%s", filepath.Join(viper.GetString("resource.image_path"), "book", fmt.Sprintf("%d", bookID), fmt.Sprintf("book_%d.webp", bookID)))
```

**关键修复**: 使用 `file://` 协议确保浏览器能正确加载本地图片

### 4. 添加调试日志

**文件**: `internal/numind/biz/markdown/lightweight_renderer.go`

**新增内容**:
```go
// 添加调试日志
log.C(ctx).Infow("替换封面图片占位符",
    "placeholder_html", placeholderHTML,
    "image_html", imageHTML,
    "cover_image_path", coverImagePath)

result := strings.Replace(htmlContent, placeholderHTML, imageHTML, 1)

// 检查是否成功替换
if result == htmlContent {
    log.C(ctx).Warnw("占位符替换失败，未找到匹配的占位符")
} else {
    log.C(ctx).Infow("占位符替换成功")
}
```

## 验证结果

### 测试场景1：模板背景获取
- ✅ 模板背景获取成功
- ✅ 背景图片路径正确

### 测试场景2：Book图片路径验证
- ✅ Book图片路径构建正确
- ✅ 使用 `file://` 协议
- ✅ 图片文件存在验证

### 测试场景3：封面卡片HTML生成
- ✅ HTML结构完整
- ✅ 包含背景层和内容层
- ✅ 包含图片占位符和标题区域

### 测试场景4：占位符替换验证
- ✅ 找到匹配的占位符
- ✅ 占位符替换成功
- ✅ 图片URL正确插入

### 测试场景5：CSS语法验证
- ✅ CSS语法正确
- ✅ 背景图片设置正确
- ✅ 层级设置正确

## 技术细节

### 1. 层级结构
```
封面卡片
├── 背景层 (z-index: 1) - 背景图完全覆盖
└── 内容层 (z-index: 2) - 图片和标题
    ├── 图片区域 (65%) - 上半部分
    └── 标题区域 (35%) - 下半部分
```

### 2. 图片路径处理
- **模板背景**: 使用 `file://` 协议加载本地背景图片
- **封面图片**: 使用 `file://` 协议加载book图片
- **错误处理**: 图片加载失败时显示占位符

### 3. 占位符替换机制
- **占位符结构**: 完全匹配生成的HTML结构
- **替换逻辑**: 使用 `strings.Replace` 进行精确替换
- **调试信息**: 详细的日志记录替换过程

## 修复效果

### 修复前的问题
1. ❌ CSS格式化错误导致HTML无法正确渲染
2. ❌ 占位符不匹配导致图片无法插入
3. ❌ 图片路径错误导致无法加载book图片
4. ❌ 缺少调试信息难以排查问题

### 修复后的效果
1. ✅ CSS语法正确，HTML渲染正常
2. ✅ 占位符匹配成功，图片正确插入
3. ✅ 图片路径正确，book图片正常加载
4. ✅ 详细调试日志，便于问题排查

## 总结

通过这次修复，解决了封面卡片渲染中的关键问题：

1. **CSS格式化错误**：修复了 `fmt.Sprintf` 中百分比符号的转义问题
2. **占位符匹配问题**：确保占位符HTML结构与生成的一致
3. **图片路径问题**：使用正确的 `file://` 协议加载本地图片
4. **调试信息不足**：添加了详细的日志记录

现在封面卡片能够正确显示：
- **背景图在底层**：完全覆盖整个卡片
- **book图片在上层**：正确显示在图片区域
- **标题在上层**：显示在标题区域
- **层级结构清晰**：背景和内容完全分离

封面卡片的渲染问题已经完全解决，能够正确包含book路径下的图片并显示完整的层级结构。
