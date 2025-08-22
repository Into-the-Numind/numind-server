package card

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// WebPRenderer WebP无头浏览器渲染器
type WebPRenderer struct {
	config  *pagination.PaginationConfig
	quality int
	timeout time.Duration
}

// NewWebPRenderer 创建新的WebP渲染器
func NewWebPRenderer(config *pagination.PaginationConfig) *WebPRenderer {
	return &WebPRenderer{
		config:  config,
		quality: 85,
		timeout: 30 * time.Second,
	}
}

// SetQuality 设置WebP质量
func (r *WebPRenderer) SetQuality(quality int) {
	if quality < 1 {
		quality = 1
	} else if quality > 100 {
		quality = 100
	}
	r.quality = quality
}

// RenderCardToImage 将卡片渲染为WebP图片
func (r *WebPRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	log.C(context.Background()).Infow("开始WebP渲染",
		"card_id", card.ID,
		"content_length", len(card.ProcessedText),
		"quality", r.quality)

	// 解析卡片数据
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		return nil, fmt.Errorf("failed to parse card data: %v", err)
	}

	// 生成HTML内容
	htmlContent := r.generateHTML(elements)

	// 使用无头浏览器渲染为WebP
	imageData, err := r.renderWithHeadlessBrowser(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	// 保存WebP图片
	imageURL, err := r.saveWebPImage(imageData, card.ID)
	if err != nil {
		return nil, err
	}

	return &RenderedCard{
		CardID:    card.ID,
		ImageURL:  imageURL,
		Width:     r.config.Card.Width,
		Height:    r.config.Card.Height,
		SortOrder: card.SortOrder,
	}, nil
}

// generateHTML 生成HTML内容
func (r *WebPRenderer) generateHTML(elements []pagination.Element) string {

	var html strings.Builder

	html.WriteString(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Card Content</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'Helvetica Neue', Helvetica, Arial, sans-serif;
            font-size: 16px;
            line-height: 1.6;
            color: #333333;
            background-color: #ffffff;
            width: ` + fmt.Sprintf("%dpx", r.config.Card.Width) + `;
            height: ` + fmt.Sprintf("%dpx", r.config.Card.Height) + `;
            overflow: hidden !important;
            padding: ` + fmt.Sprintf("%dpx", r.config.Card.Padding.Top) + ` ` + fmt.Sprintf("%dpx", r.config.Card.Padding.Right) + ` ` + fmt.Sprintf("%dpx", r.config.Card.Padding.Bottom) + ` ` + fmt.Sprintf("%dpx", r.config.Card.Padding.Left) + `;
        }

        .card-container {
            width: 100%;
            height: 100%;
            display: flex;
            flex-direction: column;
            justify-content: flex-start;
            align-items: flex-start;
        }

        .element {
            width: 100%;
            margin-bottom: 16px;
        }

        .title {
            font-size: 32px;
            font-weight: 600;
            color: #333333;
            line-height: 1.2;
        }

        .subtitle {
            font-size: 24px;
            font-weight: 500;
            color: #666666;
            line-height: 1.3;
        }

        .text {
            font-size: 18px;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
        }

        .list {
            font-size: 18px;
            color: #333333;
            line-height: 1.6;
        }

        .list-item {
            margin-bottom: 8px;
            padding-left: 20px;
            position: relative;
        }

        .list-item::before {
            content: "•";
            position: absolute;
            left: 0;
            color: #666666;
        }

        .quote {
            font-size: 18px;
            color: #1E90FF;
            font-style: italic;
            line-height: 1.6;
            padding: 16px;
            background: linear-gradient(135deg, #f8f9fa 0%, #e9ecef 100%);
            border-left: 4px solid #1E90FF;
            border-radius: 4px;
            margin: 16px 0;
        }

        .code {
            font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
            font-size: 16px;
            background-color: #f6f8fa;
            padding: 16px;
            border-radius: 6px;
            border: 1px solid #e1e4e8;
        }
    </style>
</head>
<body>
    <div class="card-container">`)

	// 渲染元素
	for _, element := range elements {
		html.WriteString(r.renderElement(element))
	}

	html.WriteString(`
    </div>
</body>
</html>`)

	return html.String()
}

// renderElement 渲染单个元素
func (r *WebPRenderer) renderElement(element pagination.Element) string {
	switch element.Type {
	case "title":
		return fmt.Sprintf(`<div class="element title">%s</div>`, r.escapeHTML(element.Content))
	case "subtitle":
		return fmt.Sprintf(`<div class="element subtitle">%s</div>`, r.escapeHTML(element.Content))
	case "text":
		return fmt.Sprintf(`<div class="element text">%s</div>`, r.processTextContent(element.Content))
	case "list":
		return r.renderList(element.Content)
	case "quote":
		return fmt.Sprintf(`<div class="element quote">%s</div>`, r.escapeHTML(element.Content))
	case "code":
		return fmt.Sprintf(`<div class="element code">%s</div>`, r.escapeHTML(element.Content))
	default:
		return fmt.Sprintf(`<div class="element text">%s</div>`, r.escapeHTML(element.Content))
	}
}

// renderList 渲染列表
func (r *WebPRenderer) renderList(content interface{}) string {
	var html strings.Builder
	html.WriteString(`<div class="element list">`)

	if listItems, ok := content.([]interface{}); ok {
		for _, item := range listItems {
			html.WriteString(fmt.Sprintf(`<div class="list-item">%s</div>`, r.escapeHTML(fmt.Sprintf("%v", item))))
		}
	} else {
		html.WriteString(fmt.Sprintf(`<div class="list-item">%s</div>`, r.escapeHTML(fmt.Sprintf("%v", content))))
	}

	html.WriteString(`</div>`)
	return html.String()
}

// processTextContent 处理文本内容
func (r *WebPRenderer) processTextContent(content interface{}) string {
	text := fmt.Sprintf("%v", content)

	// 处理粗体 **text**
	text = strings.ReplaceAll(text, "**", "<strong>")
	text = strings.ReplaceAll(text, "**", "</strong>")

	// 处理斜体 *text*
	text = strings.ReplaceAll(text, "*", "<em>")
	text = strings.ReplaceAll(text, "*", "</em>")

	return text
}

// escapeHTML 转义HTML特殊字符
func (r *WebPRenderer) escapeHTML(content interface{}) string {
	text := fmt.Sprintf("%v", content)
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	text = strings.ReplaceAll(text, "\"", "&quot;")
	text = strings.ReplaceAll(text, "'", "&#39;")
	return text
}

// renderWithHeadlessBrowser 使用无头浏览器渲染HTML为WebP
func (r *WebPRenderer) renderWithHeadlessBrowser(htmlContent string) ([]byte, error) {
	log.C(context.Background()).Infow("开始WebP无头浏览器渲染",
		"html_length", len(htmlContent),
		"quality", r.quality,
		"timeout", r.timeout)

	// 创建临时HTML文件
	tempFile, err := os.CreateTemp("", "card_webp_*.html")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %v", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// 写入HTML内容
	if _, err := tempFile.WriteString(htmlContent); err != nil {
		return nil, fmt.Errorf("failed to write HTML to temp file: %v", err)
	}

	// 获取文件的绝对路径
	absPath, err := filepath.Abs(tempFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %v", err)
	}

	fileURL := "file://" + absPath
	log.C(context.Background()).Infow("HTML文件准备完成",
		"temp_file", tempFile.Name(),
		"abs_path", absPath,
		"file_url", fileURL)

	// 创建Chrome选项
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("disable-features", "VizDisplayCompositor"),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", r.config.Card.Width, r.config.Card.Height)),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-plugins", true),
		chromedp.Flag("disable-images", false),
		chromedp.Flag("disable-javascript", false),
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("disable-font-subpixel-positioning", true),
	)

	// 创建Chrome实例
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	// 创建Chrome任务
	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 设置超时
	ctx, cancel := context.WithTimeout(taskCtx, r.timeout)
	defer cancel()

	var imageData []byte

	// 执行渲染任务
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(r.config.Card.Width), int64(r.config.Card.Height)),
		chromedp.Navigate(fileURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(5*time.Second), // 增加等待时间，确保字体和样式加载完成
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.C(context.Background()).Infow("页面加载完成，开始截图")

			// 调试：检查页面内容
			var bodyText string
			if err := chromedp.Text("body", &bodyText).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面内容检查",
					"body_length", len(bodyText))
				if len(bodyText) > 200 {
					log.C(context.Background()).Infow("页面内容预览",
						"preview", bodyText[:200])
				}
			} else {
				log.C(context.Background()).Warnw("获取页面内容失败", "error", err)
			}

			// 检查页面是否有内容
			var hasContent bool
			if err := chromedp.Evaluate(`document.body.children.length > 0`, &hasContent).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面内容检查", "has_content", hasContent)
			}

			// 等待字体加载
			if err := chromedp.Evaluate(`document.fonts.ready`, nil).Do(ctx); err == nil {
				log.C(context.Background()).Infow("字体加载完成")
			}

			// 强制重绘页面
			if err := chromedp.Evaluate(`document.body.style.display='none';document.body.offsetHeight;document.body.style.display=''`, nil).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面重绘完成")
			}

			// 先尝试PNG格式截图
			var screenshotErr error
			imageData, screenshotErr = page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithQuality(90).
				Do(ctx)

			if screenshotErr != nil {
				log.C(context.Background()).Errorw("PNG截图失败，尝试WebP", "error", screenshotErr)

				// 如果PNG失败，尝试WebP格式
				imageData, screenshotErr = page.CaptureScreenshot().
					WithFormat(page.CaptureScreenshotFormatWebp).
					WithQuality(int64(r.quality)).
					Do(ctx)

				if screenshotErr != nil {
					return fmt.Errorf("both PNG and WebP screenshot failed: %v", screenshotErr)
				}
			}

			log.C(context.Background()).Infow("截图完成",
				"image_size", len(imageData),
				"format", "PNG/WebP")

			return nil
		}),
	)

	if err != nil {
		log.C(context.Background()).Errorw("无头浏览器渲染失败", "error", err)
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	log.C(context.Background()).Infow("WebP渲染完成", "image_size", len(imageData))
	return imageData, nil
}

// saveWebPImage 保存WebP图片
func (r *WebPRenderer) saveWebPImage(imageData []byte, cardID uint) (string, error) {
	// 创建输出目录
	outputDir := filepath.Join("res", "upload", "card")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %v", err)
	}

	// 生成文件名
	fileName := fmt.Sprintf("card_%d.webp", cardID)
	filePath := filepath.Join(outputDir, fileName)

	// 写入文件
	if err := os.WriteFile(filePath, imageData, 0644); err != nil {
		return "", fmt.Errorf("failed to write WebP file: %v", err)
	}

	// 返回URL路径
	return fmt.Sprintf("/res/upload/card/%s", fileName), nil
}

// SetConfig 更新配置
func (r *WebPRenderer) SetConfig(config *pagination.PaginationConfig) {
	r.config = config
}

// GetConfig 获取当前配置
func (r *WebPRenderer) GetConfig() *pagination.PaginationConfig {
	return r.config
}

// IsAvailable 检查渲染器是否可用
func (r *WebPRenderer) IsAvailable() bool {
	// 检查Chrome是否可用
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 设置短超时进行测试
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return chromedp.Run(testCtx, chromedp.Navigate("data:text/html,<html><body>test</body></html>")) == nil
}

// GetVersion 获取版本信息
func (r *WebPRenderer) GetVersion() string {
	return "WebP-Renderer v1.0.0"
}
