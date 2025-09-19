package card

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/spf13/viper"
)

// CoverRenderer 封面卡片渲染器
type CoverRenderer struct {
	config             *pagination.PaginationConfig
	templateBackground string
}

// NewCoverRenderer 创建新的封面渲染器
func NewCoverRenderer(config *pagination.PaginationConfig) *CoverRenderer {
	return &CoverRenderer{
		config: config,
	}
}

// SetTemplateBackground 设置模板背景
func (r *CoverRenderer) SetTemplateBackground(background string) error {
	r.templateBackground = background
	return nil
}

// CoverCardData 封面卡片数据结构
type CoverCardData struct {
	Title      string `json:"title"`
	ImageURL   string `json:"image_url,omitempty"`
	Background string `json:"background,omitempty"`
}

// GetCoverConfig 获取封面专用配置（3:4比例，与内容页保持一致）
func GetCoverConfig() *pagination.PaginationConfig {
	config := pagination.GetDefaultConfig()
	// 严格保持与内容页一致的3:4比例（1080x1440）
	config.Card.Width = 1080
	config.Card.Height = 1440
	return config
}

// GetCoverTitleConfig 获取封面标题配置
func GetCoverTitleConfig() (fontSize int, lineHeight int, color string) {
	// 默认值
	fontSize = 48
	lineHeight = 62
	color = "#2c3e50"

	// 从配置中读取
	if viper.IsSet("special_rules.cover_card.title_section.font_size") {
		fontSize = viper.GetInt("special_rules.cover_card.title_section.font_size")
	}
	if viper.IsSet("special_rules.cover_card.title_section.line_height") {
		lineHeight = viper.GetInt("special_rules.cover_card.title_section.line_height")
	}
	if viper.IsSet("special_rules.cover_card.title_section.color") {
		color = viper.GetString("special_rules.cover_card.title_section.color")
	}

	return fontSize, lineHeight, color
}

// RenderCoverCardToImage 将封面卡片渲染为图片
func (r *CoverRenderer) RenderCoverCardToImage(card *model.CardM) (*RenderedCard, error) {
	// 解析卡片数据
	var elements []map[string]interface{}
	var coverData CoverCardData

	// 如果ProcessedText为空，说明这是从book创建时的封面卡片
	if card.ProcessedText == "" {
		// 从book信息中获取数据（这里需要从外部传入）
		// 暂时使用默认数据
		coverData.Title = "封面标题"
		coverData.ImageURL = ""
	} else {
		// 解析ProcessedText中的数据
		if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
			return nil, fmt.Errorf("failed to parse cover card data: %v", err)
		}

		// 提取封面数据
		for _, element := range elements {
			switch element["type"] {
			case "title":
				if title, ok := element["content"].(string); ok {
					coverData.Title = title
				}
			case "image":
				if imageURL, ok := element["content"].(string); ok {
					coverData.ImageURL = imageURL
				}
			case "background":
				if background, ok := element["content"].(string); ok {
					coverData.Background = background
				}
			}
		}
	}

	// 使用封面专用配置
	coverConfig := GetCoverConfig()

	// 生成HTML内容
	htmlContent := r.GenerateCoverHTML(coverData, coverConfig)

	// 使用无头浏览器渲染
	imageData, err := r.renderWithHeadlessBrowser(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	// 保存图片
	imageURL, err := r.saveImageFromData(imageData, card.ID)
	if err != nil {
		return nil, err
	}

	return &RenderedCard{
		CardID:    card.ID,
		ImageURL:  imageURL,
		Width:     coverConfig.Card.Width,
		Height:    coverConfig.Card.Height,
		SortOrder: card.SortOrder,
	}, nil
}

// RenderCoverCardFromBook 从book信息创建封面卡片
func (r *CoverRenderer) RenderCoverCardFromBook(card *model.CardM, bookTitle string, bookImageURL string) (*RenderedCard, error) {
	// 创建封面数据
	coverData := CoverCardData{
		Title:    bookTitle,
		ImageURL: bookImageURL,
	}

	// 使用封面专用配置
	coverConfig := GetCoverConfig()

	// 生成HTML内容
	htmlContent := r.GenerateCoverHTML(coverData, coverConfig)

	// 使用无头浏览器渲染
	imageData, err := r.renderWithHeadlessBrowser(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	// 保存图片
	imageURL, err := r.saveImageFromData(imageData, card.ID)
	if err != nil {
		return nil, err
	}

	return &RenderedCard{
		CardID:    card.ID,
		ImageURL:  imageURL,
		Width:     coverConfig.Card.Width,
		Height:    coverConfig.Card.Height,
		SortOrder: card.SortOrder,
	}, nil
}

// GenerateCoverHTML 生成封面HTML内容
func (r *CoverRenderer) GenerateCoverHTML(coverData CoverCardData, config *pagination.PaginationConfig) string {
	// 处理背景样式 - 优先使用模板背景，如果没有则使用白色背景
	backgroundStyle := ""
	if r.templateBackground != "" {
		// 使用模板背景图片
		backgroundStyle = fmt.Sprintf("background: url('file://%s') center center / cover no-repeat;", r.templateBackground)
	} else {
		// 使用默认白色背景
		backgroundStyle = "background: #ffffff;"
	}

	// 获取封面标题配置
	fontSize, lineHeight, color := GetCoverTitleConfig()

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>Cover Card</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        /* 思源宋体字体定义 - 使用本地字体文件 */
        @font-face {
            font-family: "SourceHanSerifSC";
            src: url("file:///usr/share/fonts/truetype/SourceHanSerifSC-Regular.otf") format("opentype"),
                 local("Source Han Serif SC"),
                 local("SourceHanSerifSC"),
                 local("STFangsong"),
                 local("Noto Sans CJK SC"),
                 local("PingFang SC"),
                 local("Hiragino Sans GB"),
                 local("Microsoft YaHei"),
                 local("sans-serif");
            font-weight: normal;
            font-style: normal;
        }
        
        @font-face {
            font-family: "SourceHanSerifSC";
            src: url("file:///usr/share/fonts/truetype/SourceHanSerifSC-Bold.otf") format("opentype"),
                 local("Source Han Serif SC Bold"),
                 local("SourceHanSerifSC-Bold"),
                 local("STFangsong"),
                 local("Noto Sans CJK SC Semibold"),
                 local("PingFang SC"),
                 local("Hiragino Sans GB"),
                 local("Microsoft YaHei Bold"),
                 local("sans-serif");
            font-weight: bold;
            font-style: normal;
        }
        
        html, body {
            width: %dpx;
            height: %dpx;
            margin: 0;
            padding: 0;
            overflow: hidden;
            font-family: "SourceHanSerifSC", "STFangsong", "PingFang SC", "Helvetica Neue", Arial, sans-serif;
        }
        
        /* 封面容器 - 背景图在底层，内容在上层 */
        .cover-container {
            width: 100%;
            height: 100%;
            position: relative;
            %s
            background-size: cover !important;
            background-position: center center !important;
            background-repeat: no-repeat !important;
        }
        
        /* 背景层：背景图在最后一层 */
        .cover-background-layer {
            position: absolute;
            top: 0;
            left: 0;
            width: 100%;
            height: 100%;
            z-index: 1;
            background: inherit;
            background-size: cover !important;
            background-position: center center !important;
            background-repeat: no-repeat !important;
        }
        
        /* 内容层：只显示标题 */
        .cover-content-layer {
            position: relative;
            width: 100%;
            height: 100%;
            z-index: 2;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        
        /* 标题区域 - 完全居中 */
        .title-section {
            display: flex;
            align-items: center;
            justify-content: center;
            position: relative;
            width: 100%;
            height: 100%;
        }
        
        .title-container {
            text-align: justify;
            padding: 40px;
            width: 100%;
            max-width: 90%;
        }
        
        .title {
            font-size: %dpx;
            font-weight: bold;
            color: %s;
            line-height: %dpx;
            margin: 0;
            text-shadow: 0 2px 4px rgba(0,0,0,0.1);
            word-wrap: break-word;
            hyphens: auto;
        }
        
        .cover-image {
            width: 100%;
            height: 100%;
            object-fit: cover;
            object-position: center;
        }
        
        .image-placeholder {
            width: 80%;
            height: 80%;
            background: #f8f9fa;
            border: 2px dashed #dee2e6;
            border-radius: 12px;
            display: flex;
            flex-direction: column;
            align-items: center;
            justify-content: center;
            color: #6c757d;
            font-size: 24px;
            font-weight: bold;
            text-align: center;
        }
        
        .placeholder-icon {
            font-size: 48px;
            margin-bottom: 16px;
            opacity: 0.8;
        }
        
        .placeholder-text {
            font-size: 18px;
            opacity: 0.9;
        }
        
        /* 响应式调整 */
        @media (max-width: 768px) {
            .title {
                font-size: %dpx;
            }
        }
    </style>
</head>
<body>
    <div class="cover-container">
        <!-- 背景层：背景图在最后一层 -->
        <div class="cover-background-layer"></div>
        
        <!-- 内容层：只显示标题 -->
        <div class="cover-content-layer">
            <div class="title-section">
                <div class="title-container">
                    <h1 class="title">%s</h1>
                </div>
            </div>
        </div>
    </div>
</body>
</html>`, config.Card.Width, config.Card.Height,
		backgroundStyle,
		fontSize, color, lineHeight, fontSize*3/4, // 字体大小、颜色、行高、响应式字体大小（75%）
		coverData.Title)

	return html
}

// generateImageHTML 生成图片HTML
func (r *CoverRenderer) generateImageHTML(imageURL string) string {
	if imageURL == "" {
		return `<div class="image-placeholder">
            <div class="placeholder-icon">🖼️</div>
            <div class="placeholder-text">封面图片</div>
        </div>`
	}

	// 根据 URL 类型决定如何拼接：
	// 1) http/https/data URL：直接使用
	// 2) 绝对文件路径：加上 file:// 前缀
	// 3) 相对文件路径：转为绝对路径再加 file://
	absoluteSrc := imageURL
	lower := strings.ToLower(imageURL)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") {
		// 远程或 data URL，保持原样
		absoluteSrc = imageURL
	} else if filepath.IsAbs(imageURL) {
		// 本地绝对路径，添加 file:// 前缀
		absoluteSrc = "file://" + imageURL
	} else {
		// 相对路径，转换为绝对路径
		if absPath, err := filepath.Abs(imageURL); err == nil {
			absoluteSrc = "file://" + absPath
		} else {
			// 回退到原值
			absoluteSrc = imageURL
		}
	}

	return fmt.Sprintf(`<img src="%s" class="cover-image" alt="封面图片">`, absoluteSrc)
}

// formatBackgroundStyle 将背景图路径转为内联 CSS 样式，支持 http(s)、data、本地绝对/相对路径
func formatBackgroundStyle(background string) string {
	if strings.TrimSpace(background) == "" {
		return ""
	}
	src := background
	lower := strings.ToLower(background)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") {
		// remote or data url
		src = background
	} else if filepath.IsAbs(background) {
		src = "file://" + background
	} else {
		if absPath, err := filepath.Abs(background); err == nil {
			src = "file://" + absPath
		}
	}
	// 背景图居中、cover 铺满
	return fmt.Sprintf("background: url('%s') center center / cover no-repeat;", src)
}

// sectionBackgroundStyle 如果提供了背景图，将其作为对应半区的背景（image 上半区 / title 下半区）
func sectionBackgroundStyle(background string, isImageSection bool) string {
	if strings.TrimSpace(background) == "" {
		// 无背景图，保持默认（上半灰色、下半白色）
		if isImageSection {
			return "background: #f5f5f5;"
		}
		return "background: #ffffff;"
	}
	src := background
	lower := strings.ToLower(background)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "data:") {
		src = background
	} else if filepath.IsAbs(background) {
		src = "file://" + background
	} else {
		if absPath, err := filepath.Abs(background); err == nil {
			src = "file://" + absPath
		}
	}
	return fmt.Sprintf("background: url('%s') center center / cover no-repeat;", src)
}

// renderWithHeadlessBrowser 使用无头浏览器渲染HTML
func (r *CoverRenderer) renderWithHeadlessBrowser(htmlContent string) ([]byte, error) {
	// 保存HTML内容到文件
	debugFile := fmt.Sprintf("debug_cover_%d.html", time.Now().Unix())
	if err := os.WriteFile(debugFile, []byte(htmlContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to save debug HTML: %v", err)
	}

	// 使用封面专用配置
	coverConfig := GetCoverConfig()

	// 使用Chrome命令行工具渲染，确保正确的尺寸
	outputFile := fmt.Sprintf("temp_cover_%d.png", time.Now().Unix())

	// 查找可用的浏览器可执行文件（兼容 macOS 与 Linux）
	chromeBin := findChromeExecutable()
	if chromeBin == "" {
		return nil, fmt.Errorf("chrome executable not found. Please set CHROME_BIN or install Google Chrome/Chromium")
	}

	// 读取额外的 flags（例如 Dockerfile 中的 CHROMIUM_FLAGS）
	extraFlags := os.Getenv("CHROMIUM_FLAGS")
	args := []string{}
	if extraFlags != "" {
		// 简单按空白分割
		for _, f := range strings.Fields(extraFlags) {
			args = append(args, f)
		}
	}

	// 追加默认稳定 flags（容器无权限启用 sandbox，会导致失败）
	args = append(args,
		"--headless",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-gpu",
		"--disable-web-security",
		"--disable-features=VizDisplayCompositor",
		fmt.Sprintf("--screenshot=%s", outputFile),
		fmt.Sprintf("--window-size=%d,%d", coverConfig.Card.Width, coverConfig.Card.Height),
		debugFile,
	)

	cmd := exec.Command(chromeBin, args...)

	// 捕获命令输出
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run Chrome command: %v, stderr: %s", err, stderr.String())
	}

	// 读取生成的图片文件
	imageData, err := os.ReadFile(outputFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read screenshot file %s: %v", outputFile, err)
	}

	// TODO:删除临时文件
	//os.Remove(outputFile)
	return imageData, nil
}

// findChromeExecutable 返回一个可用的 Chrome/Chromium 可执行路径
func findChromeExecutable() string {
	// 1) 环境变量优先
	if v := os.Getenv("CHROME_BIN"); v != "" && fileExists(v) {
		return v
	}
	if v := os.Getenv("CHROME_PATH"); v != "" && fileExists(v) {
		return v
	}

	// 2) 常见路径（macOS + Homebrew + Linux）
	candidates := []string{
		// macOS 常见安装路径
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		// Homebrew（可能是 shim 脚本）
		"/opt/homebrew/bin/chromium",
		"/usr/local/bin/chromium",
		"/usr/local/bin/google-chrome",
		// Linux 常见路径
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
	}
	for _, p := range candidates {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// saveImageFromData 从数据保存图片
func (r *CoverRenderer) saveImageFromData(imageData []byte, cardID uint) (string, error) {
	// 获取卡片图片保存路径
	cardDir := util.GetCardImagePath(cardID)
	fmt.Printf("调试：卡片保存目录: %s\n", cardDir)

	// 确保目录存在
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create card directory: %v", err)
	}
	fmt.Printf("调试：目录创建成功或已存在\n")

	// 生成文件名 - 改为webp格式
	filename := fmt.Sprintf("card_%d.webp", cardID)
	filepath := filepath.Join(cardDir, filename)
	fmt.Printf("调试：文件完整路径: %s\n", filepath)

	// 创建文件
	file, err := os.Create(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to create image file: %v", err)
	}
	defer file.Close()
	fmt.Printf("调试：文件创建成功\n")

	// 将PNG数据转换为webp格式
	if err := r.convertToWebP(imageData, file); err != nil {
		return "", fmt.Errorf("failed to convert to webp: %v", err)
	}
	fmt.Printf("调试：webp转换成功\n")

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	fmt.Printf("调试：返回的图片URL: %s\n", imageURL)
	return imageURL, nil
}

// convertToWebP 将PNG数据转换为webp格式
func (r *CoverRenderer) convertToWebP(pngData []byte, outputFile *os.File) error {
	// 解码PNG数据
	img, _, err := image.Decode(bytes.NewReader(pngData))
	if err != nil {
		return fmt.Errorf("failed to decode PNG data: %v", err)
	}

	// 使用cwebp命令行工具转换为webp格式，确保高质量输出
	// 创建临时PNG文件
	tempPNG := fmt.Sprintf("/tmp/temp_%d.png", time.Now().UnixNano())
	tempFile, err := os.Create(tempPNG)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %v", err)
	}

	// 将图片保存为临时PNG文件
	if err := png.Encode(tempFile, img); err != nil {
		tempFile.Close()
		os.Remove(tempPNG)
		return fmt.Errorf("failed to encode temp PNG: %v", err)
	}

	// 关闭临时文件，确保数据写入
	tempFile.Close()

	// 验证临时PNG文件是否创建成功
	if info, err := os.Stat(tempPNG); err != nil || info.Size() == 0 {
		os.Remove(tempPNG)
		return fmt.Errorf("temp PNG file creation failed or is empty: %v", err)
	}

	// 使用cwebp转换，设置高质量参数
	// 直接输出到目标文件路径
	outputPath := outputFile.Name()
	cmd := exec.Command("cwebp", "-q", "95", "-m", "6", "-af", "-f", "50", "-sharpness", "0", tempPNG, "-o", outputPath)

	// 捕获命令输出用于调试
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tempPNG)
		return fmt.Errorf("failed to convert to webp: %v, output: %s", err, string(output))
	}

	// 清理临时文件
	os.Remove(tempPNG)

	return nil
}
