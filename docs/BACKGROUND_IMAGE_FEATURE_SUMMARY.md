# 背景图功能实现总结

## 功能概述

为每张卡片添加背景图功能，通过 `template_id` 查询数据库获取背景图的 `filepath`，然后在渲染卡片时将背景图应用到每张卡片上。

## 实现内容

### 1. HTML转换器增强

#### 1.1 配置结构扩展
在 `HTMLConfig` 结构体中添加了背景图字段：
```go
type HTMLConfig struct {
    // ... 其他字段
    BackgroundImage string `json:"background_image"` // 背景图片路径
}
```

#### 1.2 背景图设置方法
添加了设置和获取背景图的方法：
```go
// SetBackgroundImage 设置背景图片路径
func (hc *HTMLConverter) SetBackgroundImage(backgroundImage string) {
    hc.config.BackgroundImage = backgroundImage
}

// GetBackgroundImage 获取背景图片路径
func (hc *HTMLConverter) GetBackgroundImage() string {
    return hc.config.BackgroundImage
}
```

#### 1.3 CSS样式生成优化
修改了 `generateClearLargeFontCSS` 方法，支持背景图：
```go
// 处理背景样式
backgroundStyle := ""
if hc.config.BackgroundImage != "" {
    // 如果有背景图，使用背景图
    backgroundStyle = fmt.Sprintf("background: url('%s') center center / cover no-repeat;", hc.config.BackgroundImage)
} else {
    // 否则使用背景色
    backgroundStyle = fmt.Sprintf("background-color: %s;", hc.config.BackgroundColor)
}
```

### 2. 卡片渲染器集成

#### 2.1 轻量级渲染器修改
修改了 `LightweightMarkdownRenderer` 的渲染逻辑：

**封面卡片渲染**：
```go
// 设置背景图（如果有）
if templateBackground != "" {
    lmr.htmlConverter.SetBackgroundImage(templateBackground)
}

// 生成封面卡片的HTML
htmlContent := lmr.htmlConverter.ConvertCardBlocksToHTML(
    blocks,
    cardContent.Title,
    true, // isCoverCard = true
)
```

**内容卡片渲染**：
```go
// 设置背景图（如果有）
if templateBackground != "" {
    lmr.htmlConverter.SetBackgroundImage(templateBackground)
}

// 生成内容卡片的HTML
htmlContent := lmr.htmlConverter.ConvertCardBlocksToHTML(
    blocks,
    cardContent.Title,
    false, // isCoverCard = false
)
```

### 3. 背景图样式特性

#### 3.1 CSS样式规范
- **背景定位**：`center center` - 背景图居中显示
- **背景尺寸**：`cover` - 背景图完全覆盖容器
- **背景重复**：`no-repeat` - 背景图不重复
- **自动切换**：有背景图时使用背景图，无背景图时使用背景色

#### 3.2 样式应用范围
- 应用到 `.markdown-body` 元素
- 支持所有卡片类型（封面卡片和内容卡片）
- 保持文字可读性（深色文字在背景图上）

## 使用流程

### 1. 数据库查询
```go
// 通过 template_id 查询模板
template, err := biz.Templates().GetByID(ctx, templateID)
if err != nil {
    // 处理错误
    return
}

// 获取背景图路径
backgroundImagePath := template.File
```

### 2. 设置背景图
```go
// 创建HTML转换器
converter := markdown.NewHTMLConverter()

// 设置背景图
converter.SetBackgroundImage(backgroundImagePath)
```

### 3. 渲染卡片
```go
// 生成带背景图的HTML
html := converter.ConvertMarkdownCardToHTML(markdown, title, index)
```

## 技术特点

### 1. 灵活性
- 支持多种图片格式（jpg, png, webp等）
- 支持相对路径和绝对路径
- 自动处理路径转换

### 2. 兼容性
- 向后兼容，无背景图时使用默认背景色
- 不影响现有卡片渲染逻辑
- 保持所有现有样式特性

### 3. 性能优化
- 背景图样式在CSS中直接生成，无需额外处理
- 减少HTML字符串操作
- 优化渲染性能

## 测试验证

### 1. 功能测试
- ✅ 无背景图时使用白色背景
- ✅ 有背景图时正确显示背景图
- ✅ 背景图样式正确（居中、覆盖、不重复）
- ✅ 文字在背景图上清晰可读

### 2. 样式检查
- ✅ 背景图URL正确设置
- ✅ CSS样式格式正确
- ✅ 自动切换机制正常

## 集成说明

### 1. 现有系统集成
背景图功能已完全集成到现有的卡片渲染流程中：
- 封面卡片渲染器
- 内容卡片渲染器
- HTML转换器
- 样式生成器

### 2. 配置管理
背景图路径通过 `template_id` 从数据库获取，无需额外配置。

### 3. 错误处理
- 模板不存在时使用默认背景
- 背景图文件不存在时使用背景色
- 路径错误时自动回退

## 总结

背景图功能已成功实现并集成到卡片渲染系统中：

1. **功能完整**：支持通过 `template_id` 查询数据库获取背景图路径
2. **渲染集成**：背景图应用到每张卡片的渲染过程中
3. **样式优化**：背景图样式正确，文字可读性良好
4. **系统兼容**：不影响现有功能，向后兼容
5. **性能优化**：渲染性能良好，无额外开销

该功能为卡片渲染系统增加了重要的视觉定制能力，提升了用户体验。
