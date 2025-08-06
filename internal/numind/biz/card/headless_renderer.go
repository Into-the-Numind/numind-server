package card

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// SimpleHeadlessRenderer 简化版无头浏览器渲染器
type SimpleHeadlessRenderer struct {
	config *pagination.PaginationConfig
}

// NewSimpleHeadlessRenderer 创建新的简化版渲染器
func NewSimpleHeadlessRenderer(config *pagination.PaginationConfig) *SimpleHeadlessRenderer {
	return &SimpleHeadlessRenderer{
		config: config,
	}
}

// RenderCardToImage 将卡片渲染为图片
func (r *SimpleHeadlessRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	// 解析卡片数据
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		return nil, fmt.Errorf("failed to parse card data: %v", err)
	}

	// 生成简单的HTML内容
	htmlContent := r.generateSimpleHTML(elements)

	// 使用无头浏览器渲染
	imageData, err := r.renderWithHeadlessBrowser(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	// 保存图片
	imageURL, err := r.saveImage(imageData, card.ID)
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

// generateSimpleHTML 生成简单的HTML内容
func (r *SimpleHeadlessRenderer) generateSimpleHTML(elements []pagination.Element) string {
	html := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>Card Render</title>
    <style>
        body {
            margin: 0;
            padding: 60px 50px;
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
            background: #ffffff;
            color: #333333;
            line-height: 1.6;
            width: 1000px;
            height: 1333px;
            box-sizing: border-box;
            overflow: hidden;
        }
        .title {
            font-size: 64px;
            font-weight: bold;
            color: #333333;
            margin-bottom: 30px;
            text-align: justify;
            line-height: 1.4;
        }
        .subtitle {
            font-size: 48px;
            font-weight: normal;
            color: #666666;
            margin-bottom: 25px;
            text-align: justify;
            line-height: 1.5;
        }
        .body {
            font-size: 36px;
            color: #333333;
            margin-bottom: 30px;
            text-align: justify;
            line-height: 1.6;
        }
        .list {
            font-size: 36px;
            color: #333333;
            margin-bottom: 8px;
            text-align: justify;
            line-height: 1.6;
            padding-left: 20px;
        }
        .list-item {
            margin-bottom: 8px;
        }
        .quote {
            font-size: 36px;
            color: #1E90FF;
            margin-bottom: 30px;
            text-align: justify;
            line-height: 1.5;
            padding: 20px;
            background: linear-gradient(to right, #EAF2FF, #FAFCFF);
            border-left: 4px solid #1E90FF;
            font-style: italic;
        }
    </style>
</head>
<body>`

	for _, element := range elements {
		content := fmt.Sprintf("%v", element.Content)
		switch element.Type {
		case pagination.ElementTypeTitle:
			html += fmt.Sprintf(`<div class="title">%s</div>`, content)
		case pagination.ElementTypeSubtitle:
			html += fmt.Sprintf(`<div class="subtitle">%s</div>`, content)
		case pagination.ElementTypeBody:
			html += fmt.Sprintf(`<div class="body">%s</div>`, content)
		case pagination.ElementTypeList:
			// 处理列表内容
			if listItems, ok := element.Content.([]interface{}); ok {
				html += `<div class="list">`
				for _, item := range listItems {
					itemText := fmt.Sprintf("%v", item)
					html += fmt.Sprintf(`<div class="list-item">• %s</div>`, itemText)
				}
				html += `</div>`
			} else {
				html += fmt.Sprintf(`<div class="list">• %s</div>`, content)
			}
		case pagination.ElementTypeQuote:
			html += fmt.Sprintf(`<div class="quote">%s</div>`, content)
		default:
			html += fmt.Sprintf(`<div class="body">%s</div>`, content)
		}
	}

	html += `</body></html>`
	return html
}

// renderWithHeadlessBrowser 使用无头浏览器渲染HTML
func (r *SimpleHeadlessRenderer) renderWithHeadlessBrowser(htmlContent string) ([]byte, error) {
	// 保存HTML内容到文件
	debugFile := fmt.Sprintf("debug_simple_%d.html", time.Now().Unix())
	if err := os.WriteFile(debugFile, []byte(htmlContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to save debug HTML: %v", err)
	}
	fmt.Printf("调试：HTML内容已保存到 %s\n", debugFile)

	// 获取绝对路径
	absPath, err := filepath.Abs(debugFile)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %v", err)
	}

	// 创建Chrome选项
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", r.config.Card.Width, r.config.Card.Height)),
	)

	// 创建Chrome实例
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	// 创建Chrome任务
	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 设置超时
	ctx, cancel := context.WithTimeout(taskCtx, 30*time.Second)
	defer cancel()

	var imageData []byte

	// 执行渲染任务
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(r.config.Card.Width), int64(r.config.Card.Height)),
		chromedp.Navigate("file://"+absPath),
		chromedp.WaitReady("body"),
		chromedp.Sleep(2*time.Second),
		chromedp.ActionFunc(func(ctx context.Context) error {
			// 调试：检查页面内容
			var bodyText string
			if err := chromedp.Text("body", &bodyText).Do(ctx); err == nil {
				fmt.Printf("调试：页面内容长度: %d\n", len(bodyText))
			}

			// 截图
			var screenshotErr error
			imageData, screenshotErr = page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithQuality(90).
				Do(ctx)
			return screenshotErr
		}),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	fmt.Printf("调试：生成的图片大小: %d bytes\n", len(imageData))
	return imageData, nil
}

// saveImage 保存图片
func (r *SimpleHeadlessRenderer) saveImage(imageData []byte, cardID uint) (string, error) {
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

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	return imageURL, nil
}
