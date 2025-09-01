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
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// EnhancedDynamicRenderer 增强动态渲染器 - 支持动态高度和大内容量
type EnhancedDynamicRenderer struct {
	config           *pagination.DynamicPaginationConfig
	paginationEngine *pagination.DynamicPaginationEngine
	background       string
}

// NewEnhancedDynamicRenderer 创建增强动态渲染器
func NewEnhancedDynamicRenderer(config *pagination.DynamicPaginationConfig) *EnhancedDynamicRenderer {
	return &EnhancedDynamicRenderer{
		config:           config,
		paginationEngine: pagination.NewDynamicPaginationEngine(config),
		background:       "linear-gradient(135deg, #667eea 0%, #764ba2 100%)", // 默认渐变背景
	}
}

// NewEnhancedDynamicRendererWithDefaults 使用默认配置创建增强动态渲染器
func NewEnhancedDynamicRendererWithDefaults() *EnhancedDynamicRenderer {
	config := pagination.GetDynamicConfig()
	return NewEnhancedDynamicRenderer(config)
}

// RenderBookToImages 渲染整本书到图片（支持大内容量）
func (r *EnhancedDynamicRenderer) RenderBookToImages(book *model.BookM, cards []*model.CardM) ([]*RenderedCard, error) {
	fmt.Printf("🚀 增强动态渲染器开始处理书籍: %s\n", book.Title)
	fmt.Printf("📚 原始卡片数: %d\n", len(cards))

	if len(cards) == 0 {
		return []*RenderedCard{}, nil
	}

	// 收集所有内容元素
	var allElements []pagination.Element

	// 添加封面元素（如果有）
	if book.ImageUrl != "" {
		coverElement := pagination.Element{
			Type:    "image",
			Content: book.ImageUrl,
		}
		allElements = append(allElements, coverElement)
		fmt.Printf("📖 添加封面图: %s\n", book.ImageUrl)
	}

	// 如果有书籍标题，添加标题元素
	if book.Title != "" {
		titleElement := pagination.Element{
			Type:    "title",
			Content: book.Title,
		}
		allElements = append(allElements, titleElement)
		fmt.Printf("📖 添加书籍标题: %s\n", book.Title)
	}

	// 处理所有卡片内容
	for i, card := range cards {
		fmt.Printf("🔍 处理卡片 %d: %d bytes\n", i+1, len(card.ProcessedText))

		// 解析卡片数据
		var elements []pagination.Element
		if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
			fmt.Printf("⚠️ 卡片 %d JSON解析失败: %v\n", i+1, err)
			continue
		}

		// 添加卡片的所有元素
		allElements = append(allElements, elements...)
		fmt.Printf("✅ 卡片 %d 添加 %d 个元素\n", i+1, len(elements))
	}

	fmt.Printf("📝 总元素数: %d\n", len(allElements))

	// 使用动态分页引擎重新分页
	fmt.Printf("🔄 开始动态分页处理...\n")
	paginatedContent, err := r.paginationEngine.CreateOptimizedPages(allElements)
	if err != nil {
		return nil, fmt.Errorf("动态分页失败: %v", err)
	}

	fmt.Printf("📄 动态分页完成，生成 %d 张优化卡片\n", len(paginatedContent.Cards))

	// 为每张优化卡片生成图片
	var renderedCards []*RenderedCard

	for i, optimizedCard := range paginatedContent.Cards {
		fmt.Printf("🖼️ 渲染优化卡片 %d/%d\n", i+1, len(paginatedContent.Cards))

		// 计算当前卡片的动态高度
		dynamicHeight := r.paginationEngine.GetOptimizedCardHeight(optimizedCard.Elements)
		fmt.Printf("📏 卡片 %d 动态高度: %d\n", i+1, dynamicHeight)

		// 生成HTML内容
		htmlContent := r.generateEnhancedHTML(optimizedCard.Elements, dynamicHeight, i == 0)

		// 渲染图片
		imageData, err := r.renderWithDynamicHeight(htmlContent, dynamicHeight)
		if err != nil {
			fmt.Printf("⚠️ 渲染卡片 %d 失败: %v\n", i+1, err)
			continue
		}

		// 保存图片
		cardID := uint(i + 1) // 使用序号作为ID
		if len(cards) > i {
			cardID = cards[i].ID
		}

		imageURL, err := r.saveImage(imageData, cardID)
		if err != nil {
			fmt.Printf("⚠️ 保存卡片 %d 图片失败: %v\n", i+1, err)
			continue
		}

		// 创建渲染结果
		renderedCard := &RenderedCard{
			CardID:    cardID,
			ImageURL:  imageURL,
			Width:     r.config.Card.Width,
			Height:    dynamicHeight,
			SortOrder: i + 1,
		}

		renderedCards = append(renderedCards, renderedCard)
		fmt.Printf("✅ 卡片 %d 渲染完成: %s (高度: %d)\n", i+1, imageURL, dynamicHeight)
	}

	fmt.Printf("🎉 增强动态渲染完成，生成 %d 张优化图片\n", len(renderedCards))
	return renderedCards, nil
}

// RenderCardToImage 渲染单个卡片到图片（使用动态高度）
func (r *EnhancedDynamicRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	fmt.Printf("🔍 增强动态渲染器处理单个卡片 ID=%d\n", card.ID)

	// 解析卡片数据
	var elements []pagination.Element
	if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
		return nil, fmt.Errorf("failed to parse card data: %v", err)
	}

	fmt.Printf("📝 解析出 %d 个元素\n", len(elements))

	// 计算动态高度
	dynamicHeight := r.paginationEngine.GetOptimizedCardHeight(elements)
	fmt.Printf("📏 动态高度: %d\n", dynamicHeight)

	// 生成HTML内容
	htmlContent := r.generateEnhancedHTML(elements, dynamicHeight, false)

	// 渲染图片
	imageData, err := r.renderWithDynamicHeight(htmlContent, dynamicHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to render with dynamic height: %v", err)
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
		Height:    dynamicHeight,
		SortOrder: card.SortOrder,
	}, nil
}

// generateEnhancedHTML 生成增强的HTML内容
func (r *EnhancedDynamicRenderer) generateEnhancedHTML(elements []pagination.Element, height int, isCover bool) string {
	var html strings.Builder

	html.WriteString(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Enhanced Dynamic Card</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            font-family: 'Noto Sans CJK SC', 'Microsoft YaHei', 'Helvetica Neue', Arial, sans-serif;
            font-weight: 400;
            color: #333;
            line-height: 1.6;
            overflow: hidden;
        }
        
        .card {`)

	html.WriteString(fmt.Sprintf(`
            width: %dpx;
            height: %dpx;
            background: %s;
            position: relative;
            display: flex;
            flex-direction: column;
            padding: %dpx %dpx %dpx %dpx;
            overflow: hidden;
        }`, r.config.Card.Width, height, r.background,
		r.config.Card.Padding.Top, r.config.Card.Padding.Right,
		r.config.MinBottomPadding, r.config.Card.Padding.Left))

	// 添加优化的样式
	html.WriteString(`
        .content {
            flex: 1;
            display: flex;
            flex-direction: column;
            gap: 0;
            overflow: hidden;
        }
        
        .cover-image {
            width: 100%;
            max-width: 100%;
            height: auto;
            max-height: 60%;
            object-fit: cover;
            border-radius: 12px;
            margin-bottom: 20px;
        }
        
        .regular-image {
            width: 100%;
            max-width: 100%;
            height: auto;
            max-height: 400px;
            object-fit: contain;
            border-radius: 8px;
            margin: 10px 0;
        }
        
        .title {
            font-size: 64px;
            font-weight: 700;
            line-height: 90px;
            color: #ffffff;
            text-align: center;
            margin: 30px 0;
            text-shadow: 2px 2px 4px rgba(0,0,0,0.3);
        }
        
        .subtitle {
            font-size: 48px;
            font-weight: 600;
            line-height: 72px;
            color: #f0f0f0;
            text-align: justify;
            margin: 30px 0 25px 0;
        }
        
        .body {
            font-size: 36px;
            font-weight: 400;
            line-height: 58px;
            color: #ffffff;
            text-align: justify;
            margin: 30px 0;
            text-shadow: 1px 1px 2px rgba(0,0,0,0.2);
        }
        
        .list {
            font-size: 36px;
            line-height: 58px;
            color: #ffffff;
            margin: 30px 0;
        }
        
        .list-item {
            margin: 15px 0;
            padding-left: 30px;
            position: relative;
        }
        
        .list-item:before {
            content: "•";
            position: absolute;
            left: 0;
            color: #ffffff;
            font-weight: bold;
        }
        
        .quote {
            font-size: 36px;
            line-height: 58px;
            color: #e0e0e0;
            font-style: italic;
            border-left: 4px solid #ffffff;
            padding-left: 20px;
            margin: 30px 0;
            background: rgba(255,255,255,0.1);
            padding: 20px;
            border-radius: 8px;
        }
        
        .number {
            font-size: 48px;
            font-weight: 700;
            color: #ffffff;
            text-align: center;
            margin: 30px 0;
            text-shadow: 2px 2px 4px rgba(0,0,0,0.3);
        }
    </style>
</head>
<body>
    <div class="card">
        <div class="content">`)

	// 渲染内容元素
	for i, element := range elements {
		switch element.Type {
		case "image":
			imageClass := "regular-image"
			if isCover && i == 0 {
				imageClass = "cover-image"
			}
			html.WriteString(fmt.Sprintf(`<img src="%v" class="%s" alt="Image">`, element.Content, imageClass))

		case "title":
			html.WriteString(fmt.Sprintf(`<div class="title">%v</div>`, element.Content))

		case "subtitle":
			html.WriteString(fmt.Sprintf(`<div class="subtitle">%v</div>`, element.Content))

		case "body":
			html.WriteString(fmt.Sprintf(`<div class="body">%v</div>`, element.Content))

		case "quote":
			html.WriteString(fmt.Sprintf(`<div class="quote">%v</div>`, element.Content))

		case "number":
			html.WriteString(fmt.Sprintf(`<div class="number">%v</div>`, element.Content))

		case "list":
			html.WriteString(`<div class="list">`)
			if listItems, ok := element.Content.([]interface{}); ok {
				for _, item := range listItems {
					html.WriteString(fmt.Sprintf(`<div class="list-item">%v</div>`, item))
				}
			} else {
				html.WriteString(fmt.Sprintf(`<div class="list-item">%v</div>`, element.Content))
			}
			html.WriteString(`</div>`)

		default:
			html.WriteString(fmt.Sprintf(`<div class="body">%v</div>`, element.Content))
		}
	}

	html.WriteString(`
        </div>
    </div>
</body>
</html>`)

	return html.String()
}

// renderWithDynamicHeight 使用动态高度渲染
func (r *EnhancedDynamicRenderer) renderWithDynamicHeight(htmlContent string, height int) ([]byte, error) {
	fmt.Printf("🖥️ 开始动态高度渲染 (高度: %d)...\n", height)

	// 保存HTML内容到文件
	debugFile := fmt.Sprintf("debug_enhanced_dynamic_%d.html", time.Now().Unix())
	if err := os.WriteFile(debugFile, []byte(htmlContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to save debug HTML: %v", err)
	}
	defer os.Remove(debugFile) // 清理临时文件

	// 获取绝对路径
	absPath, err := filepath.Abs(debugFile)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %v", err)
	}

	// 创建Chrome选项（针对大内容量优化）
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("disable-features", "VizDisplayCompositor"),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", r.config.Card.Width, height)),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-plugins", true),
		chromedp.Flag("disable-images", false),
		chromedp.Flag("disable-javascript", false),
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("disable-font-subpixel-positioning", true),
		chromedp.Flag("max-old-space-size", "4096"), // 增加内存限制支持大内容
	)

	// 创建Chrome实例
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	// 创建Chrome任务
	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 设置超时（大内容需要更长时间）
	ctx, cancel := context.WithTimeout(taskCtx, 60*time.Second)
	defer cancel()

	var imageData []byte

	// 执行渲染任务
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(r.config.Card.Width), int64(height)),
		chromedp.Navigate("file://"+absPath),
		chromedp.WaitReady("body"),
		chromedp.Sleep(3*time.Second), // 增加等待时间确保大内容加载完成
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Printf("📸 开始截图 (尺寸: %dx%d)...\n", r.config.Card.Width, height)

			// 截图
			var screenshotErr error
			imageData, screenshotErr = page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithQuality(90).
				WithClip(&page.Viewport{
					X:      0,
					Y:      0,
					Width:  float64(r.config.Card.Width),
					Height: float64(height),
					Scale:  1,
				}).
				Do(ctx)

			if screenshotErr != nil {
				fmt.Printf("❌ 截图失败: %v\n", screenshotErr)
			} else {
				fmt.Printf("✅ 截图成功，数据大小: %d bytes\n", len(imageData))
			}

			return screenshotErr
		}),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to render with dynamic height: %v", err)
	}

	return imageData, nil
}

// saveImage 保存图片文件
func (r *EnhancedDynamicRenderer) saveImage(imageData []byte, cardID uint) (string, error) {
	// 使用配置的图片路径
	baseDir := util.GetImagePath()
	cardDir := filepath.Join(baseDir, "card", fmt.Sprintf("%d", cardID))

	if err := os.MkdirAll(cardDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %v", err)
	}

	// 生成文件名 - 使用webp格式
	filename := fmt.Sprintf("card_%d.webp", cardID)
	filePath := filepath.Join(cardDir, filename)

	// 保存文件
	if err := os.WriteFile(filePath, imageData, 0644); err != nil {
		return "", fmt.Errorf("failed to save image: %v", err)
	}

	// 返回相对URL，用于数据库存储
	imageURL := util.GetCardImageURL(cardID, filename)

	fmt.Printf("🔍 增强动态渲染器：图片保存成功\n")
	fmt.Printf("   文件路径: %s\n", filePath)
	fmt.Printf("   相对URL: %s\n", imageURL)

	return imageURL, nil
}
