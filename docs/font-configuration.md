# 字体配置说明

## 概述
本项目使用**思源宋体（SourceHanSerifSC）**作为主要中文字体，以确保在所有环境中渲染效果一致。

## 字体优先级
系统配置的字体回退链：
1. **SourceHanSerifSC** - 思源宋体（首选）
2. STFangsong - 华文仿宋
3. Noto Sans CJK SC
4. PingFang SC - 苹方
5. Microsoft YaHei - 微软雅黑

## 环境配置

### Docker环境
Docker镜像已经预装了思源宋体：
- 位置：`/usr/share/fonts/truetype/SourceHanSerifSC-*.otf`
- 包含字重：Regular, Bold
- 自动配置，无需额外操作

### 本地开发环境

#### macOS
```bash
# 安装思源宋体到本地系统
./scripts/install-sourcehan-fonts.sh

# 验证安装
./scripts/verify-font-rendering.sh
```

#### Linux
```bash
# 同样使用安装脚本
./scripts/install-sourcehan-fonts.sh

# 或手动安装
sudo apt-get install fonts-noto-cjk
```

## 验证字体渲染

### 1. 运行验证脚本
```bash
./scripts/verify-font-rendering.sh
```

该脚本会检查：
- 本地字体安装情况
- Docker容器字体配置
- 配置文件字体设置
- 代码中的字体配置

### 2. Chrome DevTools验证
1. 打开渲染的页面
2. 打开DevTools (F12)
3. 在Elements面板选中文字元素
4. 在Computed面板查看"Rendered Fonts"
5. 应显示"SourceHanSerifSC"

## 配置文件

字体配置位于各环境配置文件中：
```yaml
# config_*.yaml
special_rules:
  fonts:
    family: "'SourceHanSerifSC', 'STFangsong', 'PingFang SC', 'Helvetica Neue', Arial, sans-serif"
```

## 代码实现

### 封面标题渲染
文件：`internal/numind/biz/book/async_processor.go`
```go
func (p *AsyncBookProcessor) generateCoverHTML(title, imageURL, background string, bookID uint) string {
    // 字体配置
    font-family: "SourceHanSerifSC", "STFangsong", ...
}
```

### 卡片内容渲染
文件：`internal/numind/biz/markdown/html_converter.go`
```go
FontFamily: "'SourceHanSerifSC', 'STFangsong', ..."
```

## 常见问题

### Q: 本地和Docker渲染效果不一致
A: 通常是因为本地未安装思源宋体，运行安装脚本即可解决。

### Q: 字体安装后仍然不生效
A: 需要重启Chrome浏览器和应用服务。

### Q: 如何确认使用的是正确字体？
A: 使用Chrome DevTools的Computed面板查看Rendered Fonts。

## 字体文件来源
思源宋体由Adobe开源：
- GitHub: https://github.com/adobe-fonts/source-han-serif
- 许可证：SIL Open Font License

## 注意事项
1. 安装字体后需要重启浏览器
2. Chrome headless模式需要重启服务
3. Docker镜像构建时会自动下载字体
4. 本地开发建议安装完整字重（Regular, Bold, Medium, SemiBold）