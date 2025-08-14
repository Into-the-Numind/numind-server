package card

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// SimpleHeadlessRenderer 简化版无头浏览器渲染器
type SimpleHeadlessRenderer struct {
	config     *pagination.PaginationConfig
	background string
}

// 确保SimpleHeadlessRenderer实现了RendererInterface接口
var _ RendererInterface = (*SimpleRenderer)(nil)

// NewSimpleHeadlessRenderer 创建新的简化版渲染器
func NewSimpleHeadlessRenderer(config *pagination.PaginationConfig) *SimpleHeadlessRenderer {
	return &SimpleHeadlessRenderer{
		config: config,
	}
}

// SetBackground 设置全局背景图（模板 file 字段）。为空则默认白色
func (r *SimpleHeadlessRenderer) SetBackground(background string) {
	r.background = background
}

// RenderCardToImage 将卡片渲染为图片
func (r *SimpleHeadlessRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	fmt.Printf("🔍 调试：开始渲染卡片 ID=%d\n", card.ID)
	fmt.Printf("🔍 调试：卡片内容长度=%d bytes\n", len(card.ProcessedText))
	fmt.Printf("🔍 调试：渲染配置 - 宽度=%d, 高度=%d\n", r.config.Card.Width, r.config.Card.Height)
	fmt.Printf("🔍 调试：背景设置=%s\n", r.background)

	// 解析卡片数据
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		fmt.Printf("❌ 调试：JSON解析失败 - %v\n", err)
		return nil, fmt.Errorf("failed to parse card data: %v", err)
	}

	fmt.Printf("🔍 调试：解析出 %d 个元素\n", len(elements))
	for i, element := range elements {
		fmt.Printf("🔍 调试：元素[%d] - 类型=%s, 内容长度=%d\n", i, element.Type, len(fmt.Sprintf("%v", element.Content)))
	}

	// 生成简单的HTML内容
	htmlContent := r.generateSimpleHTML(elements)
	fmt.Printf("🔍 调试：生成的HTML长度=%d bytes\n", len(htmlContent))

	// 使用无头浏览器渲染
	fmt.Printf("🔍 调试：开始无头浏览器渲染...\n")
	imageData, err := r.renderWithHeadlessBrowser(htmlContent)
	if err != nil {
		fmt.Printf("❌ 调试：无头浏览器渲染失败 - %v\n", err)
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	fmt.Printf("🔍 调试：无头浏览器渲染成功，图片数据大小=%d bytes\n", len(imageData))

	// 保存图片
	fmt.Printf("🔍 调试：开始保存图片...\n")
	imageURL, err := r.saveImage(imageData, card.ID)
	if err != nil {
		fmt.Printf("❌ 调试：保存图片失败 - %v\n", err)
		return nil, err
	}

	fmt.Printf("🔍 调试：图片保存成功，URL=%s\n", imageURL)

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
	fmt.Printf("🔍 调试：开始生成HTML，元素数量=%d\n", len(elements))

	bgStyle := formatBackgroundStyle(r.background)
	if bgStyle == "" {
		bgStyle = "background: #ffffff;"
	}
	fmt.Printf("🔍 调试：背景样式=%s\n", bgStyle)

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>Card Render</title>
    <style>
        html {
            width: %[1]dpx;
            height: %[2]dpx;
            margin: 0;
            padding: 0;
        }
        body {
            margin: 0;
            padding: 60px 50px;
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
            %s
            color: #333333;
            line-height: 1.6;
            width: %[1]dpx;
            height: %[2]dpx;
            box-sizing: border-box;
            background-clip: border-box;
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
<body>`, r.config.Card.Width, r.config.Card.Height, bgStyle, r.config.Card.Width, r.config.Card.Height)

	fmt.Printf("🔍 调试：HTML头部生成完成，长度=%d bytes\n", len(html))

	for i, element := range elements {
		content := fmt.Sprintf("%v", element.Content)
		fmt.Printf("🔍 调试：处理元素[%d]，类型=%s，内容长度=%d\n", i, element.Type, len(content))

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
				for j, item := range listItems {
					itemText := fmt.Sprintf("%v", item)
					html += fmt.Sprintf(`<div class="list-item">• %s</div>`, itemText)
					fmt.Printf("🔍 调试：列表项[%d]：%s\n", j, itemText)
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
	fmt.Printf("🔍 调试：HTML生成完成，总长度=%d bytes\n", len(html))
	return html
}

// renderWithHeadlessBrowser 使用无头浏览器渲染HTML
func (r *SimpleHeadlessRenderer) renderWithHeadlessBrowser(htmlContent string) ([]byte, error) {
	fmt.Printf("🔍 调试：开始无头浏览器渲染流程\n")

	// 保存HTML内容到文件
	debugFile := fmt.Sprintf("debug_simple_%d.html", time.Now().Unix())
	if err := os.WriteFile(debugFile, []byte(htmlContent), 0644); err != nil {
		fmt.Printf("❌ 调试：保存HTML文件失败 - %v\n", err)
		return nil, fmt.Errorf("failed to save debug HTML: %v", err)
	}
	fmt.Printf("🔍 调试：HTML内容已保存到 %s\n", debugFile)

	// 获取绝对路径
	absPath, err := filepath.Abs(debugFile)
	if err != nil {
		fmt.Printf("❌ 调试：获取绝对路径失败 - %v\n", err)
		return nil, fmt.Errorf("failed to get absolute path: %v", err)
	}
	fmt.Printf("🔍 调试：HTML文件绝对路径=%s\n", absPath)

	// 创建Chrome选项 - 针对容器环境优化
	fmt.Printf("🔍 调试：创建Chrome选项...\n")
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
		chromedp.Flag("disable-images", false),     // 保持图片渲染
		chromedp.Flag("disable-javascript", false), // 保持JS支持
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("disable-font-subpixel-positioning", true),
	)
	fmt.Printf("🔍 调试：Chrome选项创建完成，窗口尺寸=%dx%d\n", r.config.Card.Width, r.config.Card.Height)

	// 创建Chrome实例
	fmt.Printf("🔍 调试：创建Chrome实例...\n")
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()
	fmt.Printf("🔍 调试：Chrome实例创建成功\n")

	// 创建Chrome任务
	fmt.Printf("🔍 调试：创建Chrome任务...\n")
	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	fmt.Printf("🔍 调试：Chrome任务创建成功\n")

	// 设置超时
	ctx, cancel := context.WithTimeout(taskCtx, 30*time.Second)
	defer cancel()
	fmt.Printf("🔍 调试：设置30秒超时\n")

	var imageData []byte

	// 执行渲染任务
	fmt.Printf("🔍 调试：开始执行渲染任务...\n")
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(r.config.Card.Width), int64(r.config.Card.Height)),
		chromedp.Navigate("file://"+absPath),
		chromedp.WaitReady("body"),
		chromedp.Sleep(2*time.Second),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Printf("🔍 调试：页面加载完成，开始截图...\n")

			// 调试：检查页面内容
			var bodyText string
			if err := chromedp.Text("body", &bodyText).Do(ctx); err == nil {
				fmt.Printf("🔍 调试：页面内容长度: %d\n", len(bodyText))
				if len(bodyText) > 200 {
					fmt.Printf("🔍 调试：页面内容预览: %s...\n", bodyText[:200])
				} else {
					fmt.Printf("🔍 调试：页面内容: %s\n", bodyText)
				}
			} else {
				fmt.Printf("⚠️ 调试：获取页面内容失败 - %v\n", err)
			}

			// 截图
			var screenshotErr error
			imageData, screenshotErr = page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithQuality(90).
				Do(ctx)

			if screenshotErr != nil {
				fmt.Printf("❌ 调试：截图失败 - %v\n", screenshotErr)
			} else {
				fmt.Printf("🔍 调试：截图成功，数据大小=%d bytes\n", len(imageData))
			}

			return screenshotErr
		}),
	)

	if err != nil {
		fmt.Printf("❌ 调试：渲染任务执行失败 - %v\n", err)
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	fmt.Printf("🔍 调试：渲染任务执行成功\n")
	fmt.Printf("🔍 调试：生成的图片大小: %d bytes\n", len(imageData))
	return imageData, nil
}

// saveImage 保存图片
func (r *SimpleHeadlessRenderer) saveImage(imageData []byte, cardID uint) (string, error) {
	fmt.Printf("🔍 调试：开始保存图片，卡片ID=%d，数据大小=%d bytes\n", cardID, len(imageData))

	// 获取卡片图片保存路径
	cardDir := util.GetCardImagePath(cardID)
	fmt.Printf("🔍 调试：卡片保存目录=%s\n", cardDir)

	// 确保目录存在
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		fmt.Printf("❌ 调试：创建目录失败 - %v\n", err)
		return "", fmt.Errorf("failed to create card directory: %v", err)
	}
	fmt.Printf("🔍 调试：目录创建成功或已存在\n")

	// 生成文件名
	filename := fmt.Sprintf("card_%d.png", cardID)
	filepath := filepath.Join(cardDir, filename)
	fmt.Printf("🔍 调试：文件完整路径=%s\n", filepath)

	// 检查目录权限
	if info, err := os.Stat(cardDir); err == nil {
		fmt.Printf("🔍 调试：目录权限=%s，所有者=%d，组=%d\n", info.Mode(), info.Sys().(*syscall.Stat_t).Uid, info.Sys().(*syscall.Stat_t).Gid)
	}

	// 创建文件
	file, err := os.Create(filepath)
	if err != nil {
		fmt.Printf("❌ 调试：创建文件失败 - %v\n", err)
		return "", fmt.Errorf("failed to create image file: %v", err)
	}
	defer file.Close()
	fmt.Printf("🔍 调试：文件创建成功\n")

	// 写入图片数据
	bytesWritten, err := file.Write(imageData)
	if err != nil {
		fmt.Printf("❌ 调试：写入图片数据失败 - %v\n", err)
		return "", fmt.Errorf("failed to write image data: %v", err)
	}
	fmt.Printf("🔍 调试：图片数据写入成功，写入字节数=%d，预期字节数=%d\n", bytesWritten, len(imageData))

	// 同步到磁盘
	if err := file.Sync(); err != nil {
		fmt.Printf("⚠️ 调试：同步到磁盘失败 - %v\n", err)
	} else {
		fmt.Printf("🔍 调试：数据已同步到磁盘\n")
	}

	// 验证文件是否真的被创建
	if info, err := os.Stat(filepath); err == nil {
		fmt.Printf("🔍 调试：文件验证成功，大小=%d bytes，权限=%s\n", info.Size(), info.Mode())
	} else {
		fmt.Printf("⚠️ 调试：文件验证失败 - %v\n", err)
	}

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	fmt.Printf("🔍 调试：返回的图片URL=%s\n", imageURL)
	return imageURL, nil
}
