# Chrome窗口大小修复总结

## 问题描述
用户反馈封面卡片（第一张卡片）的宽高比不正确，看起来是正方形而不是期望的3:4比例（1080x1440px）。

## 问题分析
通过调试发现：
1. **HTML模板配置正确**：所有尺寸设置都是1080x1440px
2. **Chrome渲染测试通过**：静态HTML文件能正确渲染为1080x1440px的PNG
3. **问题根源**：`chrome_headless_renderer.go`中缺少`--window-size`参数

## 关键发现
对比其他渲染器发现：
- ✅ `cover_renderer.go`: 有`--window-size`参数
- ✅ `render_and_measure_renderer.go`: 有`--window-size`参数  
- ✅ `headless_renderer.go`: 有`--window-size`参数
- ❌ `chrome_headless_renderer.go`: **缺少`--window-size`参数**

## 修复方案
在`chrome_headless_renderer.go`的`startChromeHeadless()`函数中添加：
```go
fmt.Sprintf("--window-size=%d,%d", r.config.Card.Width, r.config.Card.Height),
```

## 修复位置
文件：`internal/numind/biz/card/chrome_headless_renderer.go`
行数：第115行
参数：`--window-size=%d,%d`

## 修复效果
- ✅ Chrome启动时会使用正确的窗口尺寸（1080x1440px）
- ✅ 封面卡片渲染时将保持正确的3:4比例
- ✅ 所有卡片尺寸将保持一致
- ✅ 背景图片将正确覆盖整个卡片区域

## 技术细节
Chrome无头浏览器在启动时需要明确指定窗口尺寸，否则会使用默认尺寸，导致渲染结果与期望不符。通过添加`--window-size`参数，确保Chrome使用配置中指定的卡片尺寸进行渲染。

## 验证方法
1. 检查代码编译：`go build ./internal/numind/biz/card/`
2. 验证参数添加：`grep -n "window-size" chrome_headless_renderer.go`
3. 实际渲染测试：创建书籍并检查封面卡片尺寸

## 总结
这是一个关键的配置遗漏问题。虽然HTML模板和CSS都正确设置了尺寸，但Chrome启动参数缺失导致实际渲染时使用了错误的窗口尺寸。修复后，封面卡片将正确显示为3:4比例，与内容卡片保持一致。
