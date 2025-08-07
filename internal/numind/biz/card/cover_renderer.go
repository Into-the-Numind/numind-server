package card

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// GetCoverConfig 获取封面专用配置（4:3比例）
func GetCoverConfig() *pagination.PaginationConfig {
	config := pagination.GetDefaultConfig()
	// 修改为4:3比例
	config.Card.Width = 1200 // 4:3比例，宽度1200
	config.Card.Height = 900 // 高度900
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
            flex: 1;
            display: flex;
            align-items: center;
            justify-content: center;
            background: #f5f5f5;
            position: relative;
            min-height: 50%%;
        }
        
        .cover-image {
            max-width: 90%%;
            max-height: 90%%;
            object-fit: contain;
            border-radius: 8px;
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
            font-size: 48px;
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
        
        .divider {
            height: 2px;
            background: linear-gradient(90deg, transparent, #e0e0e0, transparent);
            margin: 0;
        }
    </style>
</head>
<body>
    <div class="cover-container">
        <div class="image-section">
            %s
        </div>
        <div class="divider"></div>
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

	// 将相对路径转换为绝对路径
	absolutePath := imageURL
	if !filepath.IsAbs(imageURL) {
		// 如果是相对路径，转换为绝对路径
		if absPath, err := filepath.Abs(imageURL); err == nil {
			absolutePath = "file://" + absPath
		} else {
			// 如果转换失败，使用相对路径
			absolutePath = imageURL
		}
	} else {
		// 如果是绝对路径，添加file://前缀
		absolutePath = "file://" + imageURL
	}

	return fmt.Sprintf(`<img src="%s" class="cover-image" alt="封面图片">`, absolutePath)
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
	cmd := exec.Command("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"--headless",
		"--disable-gpu",
		"--screenshot="+outputFile,
		fmt.Sprintf("--window-size=%d,%d", coverConfig.Card.Width, coverConfig.Card.Height),
		debugFile)

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

// saveImageFromData 从数据保存图片
func (r *CoverRenderer) saveImageFromData(imageData []byte, cardID uint) (string, error) {
	// 获取卡片图片保存路径
	cardDir := util.GetCardImagePath(cardID)

	// 确保目录存在
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create card directory: %v", err)
	}

	// 生成文件名
	filename := fmt.Sprintf("card_%d.png", cardID)
	filepath := filepath.Join(cardDir, filename)

	// 创建文件
	file, err := os.Create(filepath)
	if err != nil {
		return "", fmt.Errorf("failed to create image file: %v", err)
	}
	defer file.Close()

	// 写入图片数据
	if _, err := file.Write(imageData); err != nil {
		return "", fmt.Errorf("failed to write image data: %v", err)
	}

	// 确保数据写入磁盘
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("failed to sync image file: %v", err)
	}

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	return imageURL, nil
}
