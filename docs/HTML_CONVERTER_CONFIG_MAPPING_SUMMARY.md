# HTML 转换器配置映射完成总结

## 概述

成功将 `html_converter.go` 中的硬编码配置映射到 `config_local.yaml` 中，实现了通过修改配置文件来调整字体大小、行高、边距等参数的功能。

## 修改内容

### 1. 配置文件更新 (`config_local.yaml`)

添加了 `html_converter` 配置项，包含：
- 字体配置：标题、副标题、正文字体大小
- 行高配置：标题、副标题、正文行高倍数
- 边距配置：标题、副标题、正文下边距
- 分页配置：可用宽度、最大内容高度等

### 2. 代码结构更新

#### 2.1 新增配置字段
在 `HTMLConfig` 结构体中添加了 11 个新字段，用于存储分页相关的配置参数。

#### 2.2 配置加载逻辑
在 `NewHTMLConverter` 函数中添加了从 viper 配置加载的逻辑，支持从 `config_local.yaml` 读取配置值。

#### 2.3 硬编码替换
将 `splitContentByHeight` 方法中的硬编码常量替换为配置值，包括：
- 字体大小：titleFontSize, subtitleFontSize, bodyFontSize
- 行高：titleLineHeight, subtitleLineHeight, bodyLineHeight
- 边距：titleMarginBottom, subtitleMarginBottom, bodyMarginBottom
- 宽度：availableWidth

### 3. 测试验证

创建了测试程序 `cmd/test-html-converter-config/main.go`，验证配置正确加载：

```
=== HTML Converter Configuration Test ===
Font Configuration:
  Title Size: 28
  Subtitle Size: 24
  Body Size: 16
Line Height Configuration:
  Title: 1.40
  Subtitle: 1.40
  Body: 1.60
```

## 配置项对应关系

| 配置文件路径 | 代码字段 | 默认值 | 说明 |
|-------------|----------|--------|------|
| `html_converter.fonts.title_size` | `TitleFontSize` | 28 | 标题字体大小 |
| `html_converter.fonts.subtitle_size` | `SubtitleFontSize` | 24 | 副标题字体大小 |
| `html_converter.fonts.body_size` | `BodyFontSize` | 16 | 正文字体大小 |
| `html_converter.line_heights.title` | `TitleLineHeight` | 1.4 | 标题行高倍数 |
| `html_converter.line_heights.subtitle` | `SubtitleLineHeight` | 1.4 | 副标题行高倍数 |
| `html_converter.line_heights.body` | `BodyLineHeight` | 1.6 | 正文行高倍数 |
| `html_converter.margins.title_bottom` | `TitleMarginBottom` | 16 | 标题下边距 |
| `html_converter.margins.subtitle_bottom` | `SubtitleMarginBottom` | 16 | 副标题下边距 |
| `html_converter.margins.body_bottom` | `BodyMarginBottom` | 16 | 正文下边距 |

## 使用方法

现在可以通过修改 `config_local.yaml` 中的配置来调整 HTML 转换器的行为：

1. **调整字体大小**：修改 `html_converter.fonts.*_size` 值
2. **调整行高**：修改 `html_converter.line_heights.*` 值
3. **调整边距**：修改 `html_converter.margins.*_bottom` 值

## 优势

1. **灵活性**：无需修改代码即可调整样式参数
2. **可维护性**：配置集中管理，便于维护
3. **环境隔离**：不同环境可以使用不同的配置文件
4. **实时生效**：重启服务后配置立即生效
