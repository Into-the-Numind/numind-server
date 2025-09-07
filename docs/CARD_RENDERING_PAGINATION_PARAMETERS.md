# 卡片渲染和分页参数配置文档

## 概述

本文档详细列出了当前项目中与卡片渲染和分页相关的所有参数配置，包括基础配置、动态分页、样式配置、渲染策略等。

## 1. 卡片基础配置参数

### 1.1 卡片尺寸参数

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 卡片宽度 | `pagination.card.width` | 1080px | 标准卡片宽度 |
| 卡片高度 | `pagination.card.height` | 1440px | 标准卡片高度（3:4比例） |

### 1.2 卡片内边距参数

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 上内边距 | `pagination.card.padding.top` | 60px | 卡片顶部内边距 |
| 右内边距 | `pagination.card.padding.right` | 50px | 卡片右侧内边距 |
| 下内边距 | `pagination.card.padding.bottom` | 40px | 卡片底部内边距 |
| 左内边距 | `pagination.card.padding.left` | 50px | 卡片左侧内边距 |

## 2. 动态分页配置参数

### 2.1 高度控制参数

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 最小卡片高度 | `pagination.dynamic.min_height` | 720px | 最小卡片高度（标准高度的一半） |
| 最大卡片高度 | `pagination.dynamic.max_height` | 4320px | 最大卡片高度（3倍标准高度） |
| 最小底部留白 | `pagination.dynamic.min_bottom_padding` | 40px | 最小底部留白，确保文字不被遮挡 |
| 基础高度 | `pagination.dynamic.base_height` | 1440px | 基础高度（用于规范化） |

### 2.2 内容限制参数

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 每张卡片最大图片数 | `pagination.dynamic.max_images_per_card` | 5 | 单张卡片允许的最大图片数量 |
| 每张卡片最大文本长度 | `pagination.dynamic.max_text_length` | 2000字符 | 单张卡片允许的最大文本字符数 |

### 2.3 图片渲染参数

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 基础图片高度 | `pagination.dynamic.base_image_height` | 400px | 基础图片高度 |
| 图片上边距 | `pagination.dynamic.image_margin_top` | 20px | 图片上边距 |
| 图片下边距 | `pagination.dynamic.image_margin_bottom` | 20px | 图片下边距 |

## 3. 元素样式配置参数

### 3.1 标题样式 (title)

| 参数名 | 配置路径 | 本地环境 | 生产环境 | 说明 |
|--------|----------|----------|----------|------|
| 字体大小 | `pagination.styles.title.font_size` | 84px | 64px | 标题字体大小 |
| 行高 | `pagination.styles.title.line_height` | 118px | 90px | 标题行高（1.4倍） |
| 上边距 | `pagination.styles.title.margin_top` | 30px | 30px | 标题上边距 |
| 下边距 | `pagination.styles.title.margin_bottom` | 30px | 30px | 标题下边距 |
| 文字颜色 | `pagination.styles.title.color` | #333333 | #333333 | 标题文字颜色（深灰） |
| 对齐方式 | `pagination.styles.title.align` | justify | justify | 两端对齐 |

### 3.2 副标题样式 (subtitle)

| 参数名 | 配置路径 | 本地环境 | 生产环境 | 说明 |
|--------|----------|----------|----------|------|
| 字体大小 | `pagination.styles.subtitle.font_size` | 68px | 48px | 副标题字体大小 |
| 行高 | `pagination.styles.subtitle.line_height` | 102px | 72px | 副标题行高（1.5倍） |
| 上边距 | `pagination.styles.subtitle.margin_top` | 30px | 30px | 副标题上边距 |
| 下边距 | `pagination.styles.subtitle.margin_bottom` | 25px | 25px | 副标题下边距 |
| 文字颜色 | `pagination.styles.subtitle.color` | #666666 | #666666 | 副标题文字颜色（中灰） |
| 对齐方式 | `pagination.styles.subtitle.align` | justify | justify | 两端对齐 |

### 3.3 正文样式 (body)

| 参数名 | 配置路径 | 本地环境 | 生产环境 | 说明 |
|--------|----------|----------|----------|------|
| 字体大小 | `pagination.styles.body.font_size` | 56px | 36px | 正文字体大小 |
| 行高 | `pagination.styles.body.line_height` | 90px | 58px | 正文行高（1.6倍） |
| 上边距 | `pagination.styles.body.margin_top` | 30px | 30px | 正文上边距 |
| 下边距 | `pagination.styles.body.margin_bottom` | 30px | 30px | 正文下边距 |
| 文字颜色 | `pagination.styles.body.color` | #333333 | #333333 | 正文文字颜色（深灰） |
| 对齐方式 | `pagination.styles.body.align` | justify | justify | 两端对齐 |

### 3.4 列表样式 (list)

| 参数名 | 配置路径 | 本地环境 | 生产环境 | 说明 |
|--------|----------|----------|----------|------|
| 字体大小 | `pagination.styles.list.font_size` | 56px | 36px | 列表字体大小 |
| 行高 | `pagination.styles.list.line_height` | 90px | 58px | 列表行高（1.6倍） |
| 上边距 | `pagination.styles.list.margin_top` | 30px | 30px | 列表上边距 |
| 下边距 | `pagination.styles.list.margin_bottom` | 30px | 30px | 列表下边距 |
| 文字颜色 | `pagination.styles.list.color` | #333333 | #333333 | 列表文字颜色（深灰） |
| 对齐方式 | `pagination.styles.list.align` | justify | justify | 两端对齐 |
| 缩进 | `pagination.styles.list.indent` | 20px | 20px | 列表缩进 |

### 3.5 引用样式 (quote)

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 字体大小 | `pagination.styles.quote.font_size` | 46px | 引用字体大小 |
| 行高 | `pagination.styles.quote.line_height` | 74px | 引用行高（1.5倍） |
| 上边距 | `pagination.styles.quote.margin_top` | 30px | 引用上边距 |
| 下边距 | `pagination.styles.quote.margin_bottom` | 30px | 引用下边距 |
| 文字颜色 | `pagination.styles.quote.color` | #1E90FF | 引用文字颜色（蓝色） |
| 对齐方式 | `pagination.styles.quote.align` | justify | 两端对齐 |

### 3.6 标签样式 (tag)

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 字体大小 | `pagination.styles.tag.font_size` | 38px | 标签字体大小 |
| 行高 | `pagination.styles.tag.line_height` | 62px | 标签行高（1.5倍） |
| 上边距 | `pagination.styles.tag.margin_top` | 30px | 标签上边距 |
| 下边距 | `pagination.styles.tag.margin_bottom` | 30px | 标签下边距 |
| 文字颜色 | `pagination.styles.tag.color` | #1E90FF | 标签文字颜色（蓝色） |
| 对齐方式 | `pagination.styles.tag.align` | left | 左对齐 |

### 3.7 数字样式 (number)

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 字体大小 | `pagination.styles.number.font_size` | 48px | 数字字体大小 |
| 行高 | `pagination.styles.number.line_height` | 72px | 数字行高（1.5倍） |
| 上边距 | `pagination.styles.number.margin_top` | 30px | 数字上边距 |
| 下边距 | `pagination.styles.number.margin_bottom` | 30px | 数字下边距 |
| 文字颜色 | `pagination.styles.number.color` | #1E90FF | 数字文字颜色（蓝色） |
| 对齐方式 | `pagination.styles.number.align` | center | 居中对齐 |

## 4. 高级配置参数

### 4.1 字符宽度和文本处理参数

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 字符宽度系数 | `pagination.char_width_factor` | 1.05 | 中文字符宽度系数，用于文本换行计算 |
| 溢出容错比例 | `pagination.overflow_tolerance` | 0.05 | 允许内容超出可用高度的比例（5%） |
| 高利用率阈值 | `pagination.high_utilization_threshold` | 85.0 | 高利用率阈值，达到此值时创建新卡片 |
| 最小每行字符数 | `pagination.min_chars_per_line` | 20 | 最小每行字符数，防止除零错误 |
| 列表项间距 | `pagination.list_item_spacing` | 8px | 列表项之间的间距 |

### 4.2 渲染器配置参数

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 渲染器宽度 | `renderer.width` | 1080px | 渲染器输出图片宽度 |
| 渲染器高度 | `renderer.height` | 1440px | 渲染器输出图片高度 |
| 图片质量 | `renderer.quality` | 85 | 图片质量（1-100） |
| 图片格式 | `renderer.format` | "webp" | 输出图片格式 |
| 缩放比例 | `renderer.zoom` | 1.0 | 渲染缩放比例 |
| 超时时间 | `renderer.timeout_seconds` | 30秒 | 渲染超时时间 |

## 5. 渲染策略参数

### 5.1 渲染策略类型

| 策略名称 | 枚举值 | 说明 |
|----------|--------|------|
| 增强渲染策略 | `StrategyEnhanced` | 逐张渲染策略 |
| 超长图渲染策略 | `StrategySuperLongImage` | 先拼接后切分策略 |
| 精确测量策略 | `StrategyPreciseMeasurement` | 先测量后渲染策略 |

### 5.2 渲染选项参数

| 参数名 | 类型 | 说明 |
|--------|------|------|
| `Strategy` | `RenderingStrategy` | 渲染策略选择 |
| `EnableMeasurement` | `bool` | 是否启用测量功能 |
| `EnableSuperLong` | `bool` | 是否启用超长图处理 |
| `EnableOptimization` | `bool` | 是否启用优化功能 |
| `DebugMode` | `bool` | 是否启用调试模式 |

## 5. 特殊渲染规则参数

### 5.1 封面卡片配置

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 封面卡片宽度 | `special_rules.cover_card.width` | 1080px | 封面卡片宽度 |
| 封面卡片高度 | `special_rules.cover_card.height` | 1440px | 封面卡片高度（3:4比例） |
| 标题区域背景 | `special_rules.cover_card.title_section.background` | rgba(255, 255, 255, 0.9) | 标题区域半透明背景 |
| 背景模糊效果 | `special_rules.cover_card.title_section.backdrop_filter` | blur(10px) | 背景模糊效果 |
| 阴影效果 | `special_rules.cover_card.title_section.box_shadow` | 0 4px 20px rgba(0, 0, 0, 0.1) | 阴影效果 |
| 图片区域背景 | `special_rules.cover_card.image_section.background` | transparent | 图片区域透明背景 |

### 5.2 内容卡片配置

| 参数名 | 配置路径 | 默认值 | 说明 |
|--------|----------|--------|------|
| 内容安全区域边距 | `special_rules.content_card.safe_area_margin` | 30px | 内容安全区域边距 |
| 内容最大高度 | `special_rules.content_card.max_content_height` | 1200px | 内容最大高度 |
| 溢出行为 | `special_rules.content_card.overflow_behavior` | create_new_card | 溢出行为：创建新卡片 |

## 6. 分页算法参数

### 6.1 高度计算参数

| 参数名 | 计算公式 | 说明 |
|--------|----------|------|
| 可用高度 | `卡片高度 - 上边距 - 下边距` | 实际可用于内容的高度 |
| 容错空间 | `可用高度 × 5%` | 允许的溢出容错空间 |
| 字符宽度 | `字体大小 × 1.05` | 中文字符宽度估算（保守估计） |

### 6.2 分页逻辑参数

| 参数名 | 默认值 | 说明 |
|--------|--------|------|
| 利用率分析 | 当前利用率 vs 预测利用率 | 用于判断是否需要分页 |
| 溢出容错 | 5% | 允许小幅超出可用高度 |
| 行数计算 | 基于字符宽度和可用宽度 | 计算文本行数 |

## 7. 配置文件结构

### 7.1 环境配置差异

| 环境 | 字体大小差异 | 说明 |
|------|-------------|------|
| 本地环境 (local) | 比生产环境大20px | 便于本地调试和预览 |
| 开发环境 (dev) | 与生产环境相同 | 与生产环境保持一致 |
| 测试环境 (qa) | 与生产环境相同 | 与生产环境保持一致 |
| 生产环境 (prod) | 标准尺寸 | 最终用户使用的尺寸 |

### 7.2 配置加载顺序

1. 默认配置 (`GetDefaultConfig()`)
2. Viper配置加载 (`LoadConfigFromViper()`)
3. 环境特定配置覆盖
4. 运行时配置更新

## 8. 使用示例

### 8.1 基础配置示例

```yaml
pagination:
  card:
    width: 1080
    height: 1440
    padding:
      top: 60
      right: 50
      bottom: 40
      left: 50
```

### 8.2 样式配置示例

```yaml
pagination:
  styles:
    title:
      font_size: 64
      line_height: 90
      margin_top: 30
      margin_bottom: 30
      color: "#333333"
      align: "justify"
```

### 8.3 动态分页配置示例

```yaml
pagination:
  dynamic:
    min_height: 720
    max_height: 4320
    min_bottom_padding: 40
    max_images_per_card: 5
    max_text_length: 2000
    base_height: 1440
    char_width_factor: 1.05
    overflow_tolerance: 0.05
    high_utilization_threshold: 85.0
    base_image_height: 400
    image_margin_top: 20
    image_margin_bottom: 20
    min_chars_per_line: 20
    list_item_spacing: 8
```

### 8.4 高级配置示例

```yaml
pagination:
  char_width_factor: 1.05
  overflow_tolerance: 0.05
  high_utilization_threshold: 85.0
  min_chars_per_line: 20
  list_item_spacing: 8

renderer:
  width: 1080
  height: 1440
  quality: 85
  format: "webp"
  zoom: 1.0
  timeout_seconds: 30
```

## 9. 注意事项

1. **字体大小差异**：本地环境字体比生产环境大20px，便于调试
2. **高度计算**：动态分页会根据内容实际高度调整卡片高度
3. **容错机制**：允许5%的溢出容错，提高空间利用率
4. **环境配置**：不同环境使用不同的配置文件（config_local.yaml, config_dev.yaml等）
5. **渲染策略**：支持多种渲染策略，可根据内容特点选择最优策略
6. **硬编码参数已移除**：所有之前硬编码的参数现在都可以通过配置文件调整
7. **字符宽度系数**：影响文本换行和分页的准确性，建议根据实际字体调整
8. **渲染器配置**：可以通过配置文件调整输出图片的尺寸、质量和格式
9. **动态分页参数**：所有动态分页相关的参数都可以独立配置
10. **配置加载顺序**：系统会先加载默认配置，然后用配置文件中的值覆盖

---

*文档生成时间：2024年12月*
*项目：numind-server*
