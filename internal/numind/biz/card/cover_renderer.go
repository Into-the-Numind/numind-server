package card

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// CoverRenderer 封面卡片渲染器
type CoverRenderer struct {
	config *pagination.PaginationConfig
}

// NewCoverRenderer 创建新的封面渲染器
func NewCoverRenderer(config *pagination.PaginationConfig) *CoverRenderer {
	return &CoverRenderer{
		config: config,
	}
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
	htmlContent := r.generateCoverHTML(coverData, coverConfig)

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
	htmlContent := r.generateCoverHTML(coverData, coverConfig)

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

// generateCoverHTML 生成封面HTML内容
func (r *CoverRenderer) generateCoverHTML(coverData CoverCardData, config *pagination.PaginationConfig) string {
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
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
            background: #ffffff;
            color: #333333;
            width: %dpx;
            height: %dpx;
            overflow: hidden;
        }
        
        .cover-container {
            width: 100%%;
            height: 100%%;
            display: flex;
            flex-direction: column;
        }
        
        .image-section {
            height: 50%%;
            display: flex;
            align-items: center;
            justify-content: center;
            background: #f5f5f5;
            position: relative;
            overflow: hidden;
        }
        
        .cover-image {
            width: 100%%;
            height: 100%%;
            object-fit: cover;
            object-position: center;
        }
        
        .image-placeholder {
            width: 80%%;
            height: 80%%;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            border-radius: 8px;
            display: flex;
            align-items: center;
            justify-content: center;
            color: white;
            font-size: 24px;
            font-weight: bold;
        }
        
        .title-section {
            height: 50%%;
            display: flex;
            align-items: center;
            justify-content: center;
            background: #ffffff;
            padding: 40px;
            box-sizing: border-box;
        }
        
        .title-container {
            text-align: center;
        }
        
        .title {
            font-size: 64px;
            font-weight: bold;
            color: #333333;
            line-height: 1.4;
            margin: 0 0 20px 0;
            text-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        
        .decorative-line {
            width: 120px;
            height: 3px;
            background: linear-gradient(90deg, #333333, #666666);
            margin: 0 auto;
            border-radius: 2px;
        }
        
    </style>
</head>
<body>
    <div class="cover-container">
        <div class="image-section">
            %s
        </div>
        <div class="title-section">
            <div class="title-container">
                <h1 class="title">%s</h1>
                <div class="decorative-line"></div>
            </div>
        </div>
    </div>
</body>
</html>`, config.Card.Width, config.Card.Height,
		r.generateImageHTML(coverData.ImageURL),
		coverData.Title)

	return html
}

// generateImageHTML 生成图片HTML
func (r *CoverRenderer) generateImageHTML(imageURL string) string {
	if imageURL == "" {
		return `<div class="image-placeholder">万相生成的图片</div>`
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

	// 删除临时文件
	os.Remove(outputFile)
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

	// 生成文件名
	filename := fmt.Sprintf("card_%d.png", cardID)
	filepath := filepath.Join(cardDir, filename)
	fmt.Printf("调试：文件完整路径: %s\n", filepath)

	// 创建文件
	file, err := os.Create(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to create image file: %v", err)
	}
	defer file.Close()
	fmt.Printf("调试：文件创建成功\n")

	// 写入图片数据
	if _, err := file.Write(imageData); err != nil {
		return "", fmt.Errorf("failed to write image data: %v", err)
	}
	fmt.Printf("调试：图片数据写入成功，大小: %d bytes\n", len(imageData))

	// 确保数据写入磁盘
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("failed to sync image file: %v", err)
	}
	fmt.Printf("调试：文件同步到磁盘成功\n")

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	fmt.Printf("调试：返回的图片URL: %s\n", imageURL)
	return imageURL, nil
}
