package card

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/chromedp/chromedp"
)

// SuperLongImageProcessor 超长图处理器
// 实现超长图拼接和按切分点截取的功能
type SuperLongImageProcessor struct {
	config *pagination.PaginationConfig
}

// NewSuperLongImageProcessor 创建超长图处理器
func NewSuperLongImageProcessor(config *pagination.PaginationConfig) *SuperLongImageProcessor {
	return &SuperLongImageProcessor{
		config: config,
	}
}

// CardMeasurement 卡片测量数据
type CardMeasurement struct {
	CardID       uint                 `json:"card_id"`
	SortOrder    int                  `json:"sort_order"`
	Height       int                  `json:"height"`
	Elements     []pagination.Element `json:"elements"`
	IsFirstCard  bool                 `json:"is_first_card"`
	TitleContent string               `json:"title_content,omitempty"`
	ImageURL     string               `json:"image_url,omitempty"`
}

// CutPoint 切分点数据
type CutPoint struct {
	StartY     int `json:"start_y"`     // 起始Y坐标
	EndY       int `json:"end_y"`       // 结束Y坐标
	PageIndex  int `json:"page_index"`  // 页面索引
	CardHeight int `json:"card_height"` // 页面高度
}

// ProcessBookAsSuperLongImage 将整本书处理为超长图，然后按切分点截取
func (p *SuperLongImageProcessor) ProcessBookAsSuperLongImage(
	book *model.BookM,
	structuredTextArray []pagination.Element,
	imagePromptURL string,
) ([]*RenderedCard, error) {
	fmt.Printf("🚀 开始超长图处理，书籍: %s\n", book.Title)

	// 1. 测量所有卡片的高度
	measurements, err := p.measureAllCards(book, structuredTextArray, imagePromptURL)
	if err != nil {
		return nil, fmt.Errorf("测量卡片高度失败: %v", err)
	}

	// 2. 计算切分点
	cutPoints, err := p.calculateCutPoints(measurements)
	if err != nil {
		return nil, fmt.Errorf("计算切分点失败: %v", err)
	}

	// 3. 生成超长图HTML
	superLongHTML, totalHeight, err := p.generateSuperLongHTML(measurements)
	if err != nil {
		return nil, fmt.Errorf("生成超长图HTML失败: %v", err)
	}

	// 4. 渲染超长图
	superLongImageData, err := p.renderSuperLongImage(superLongHTML, totalHeight)
	if err != nil {
		return nil, fmt.Errorf("渲染超长图失败: %v", err)
	}

	// 5. 按切分点截取图片
	renderedCards, err := p.cropImagesBySubPoints(superLongImageData, cutPoints, book.ID)
	if err != nil {
		return nil, fmt.Errorf("按切分点截取失败: %v", err)
	}

	fmt.Printf("✅ 超长图处理完成，共生成 %d 张卡片\n", len(renderedCards))
	return renderedCards, nil
}

// measureAllCards 测量所有卡片的高度
func (p *SuperLongImageProcessor) measureAllCards(
	book *model.BookM,
	structuredTextArray []pagination.Element,
	imagePromptURL string,
) ([]CardMeasurement, error) {
	fmt.Printf("📏 开始测量所有卡片高度\n")

	var measurements []CardMeasurement

	// 1. 第一张卡片（特殊卡片，固定高度）
	titleContent := p.extractTitleContent(structuredTextArray)
	firstCardMeasurement := CardMeasurement{
		CardID:       book.ID*1000 + 1,
		SortOrder:    1,
		Height:       1440, // 第一张卡片固定高度
		IsFirstCard:  true,
		TitleContent: titleContent,
		ImageURL:     imagePromptURL,
	}
	measurements = append(measurements, firstCardMeasurement)

	// 2. 后续卡片（需要测量高度）
	remainingElements := p.filterOutUsedTitle(structuredTextArray)
	if len(remainingElements) > 0 {
		// 为每个元素创建单独的卡片进行测量
		for i, element := range remainingElements {
			sortOrder := i + 2
			cardID := book.ID*1000 + uint(sortOrder)

			// 测量单个元素的高度
			elementHeight, err := p.measureSingleElementHeight(element)
			if err != nil {
				fmt.Printf("⚠️ 测量元素 %d 高度失败: %v\n", i, err)
				// 使用默认高度
				elementHeight = 200
			}

			measurement := CardMeasurement{
				CardID:    cardID,
				SortOrder: sortOrder,
				Height:    elementHeight + 120, // 加上边距
				Elements:  []pagination.Element{element},
			}
			measurements = append(measurements, measurement)
		}
	}

	fmt.Printf("✅ 卡片高度测量完成，共 %d 张卡片\n", len(measurements))
	return measurements, nil
}

// calculateCutPoints 根据测量结果计算切分点
func (p *SuperLongImageProcessor) calculateCutPoints(measurements []CardMeasurement) ([]CutPoint, error) {
	fmt.Printf("📐 开始计算切分点\n")

	const outputCardHeight = 1440 // 输出卡片固定高度
	var cutPoints []CutPoint
	var currentY int
	var pageIndex int

	for _, measurement := range measurements {
		// 检查当前卡片是否需要新的输出页面
		if currentY+measurement.Height > outputCardHeight && currentY > 0 {
			// 当前页面结束，创建切分点
			cutPoint := CutPoint{
				StartY:     currentY - (currentY % outputCardHeight),
				EndY:       currentY,
				PageIndex:  pageIndex,
				CardHeight: outputCardHeight,
			}
			cutPoints = append(cutPoints, cutPoint)
			pageIndex++
			currentY = 0 // 重置到新页面开始
		}

		// 如果单张卡片高度超过输出高度，需要特殊处理
		if measurement.Height > outputCardHeight {
			fmt.Printf("⚠️ 卡片 %d 高度 %d 超过输出高度 %d，将被强制切分\n",
				measurement.CardID, measurement.Height, outputCardHeight)

			// 创建多个切分点来处理超高卡片
			remainingHeight := measurement.Height
			cardStartY := currentY

			for remainingHeight > 0 {
				cutHeight := outputCardHeight
				if remainingHeight < outputCardHeight {
					cutHeight = remainingHeight
				}

				cutPoint := CutPoint{
					StartY:     cardStartY,
					EndY:       cardStartY + cutHeight,
					PageIndex:  pageIndex,
					CardHeight: cutHeight,
				}
				cutPoints = append(cutPoints, cutPoint)

				cardStartY += cutHeight
				remainingHeight -= cutHeight
				pageIndex++
			}

			currentY = cardStartY
		} else {
			// 正常卡片，直接累加高度
			currentY += measurement.Height
		}

		fmt.Printf("📏 卡片 %d [排序 %d]: 高度=%d, 当前Y=%d\n",
			measurement.CardID, measurement.SortOrder, measurement.Height, currentY)
	}

	// 处理最后一个切分点
	if currentY > 0 {
		cutPoint := CutPoint{
			StartY:     currentY - (currentY % outputCardHeight),
			EndY:       currentY,
			PageIndex:  pageIndex,
			CardHeight: currentY % outputCardHeight,
		}
		if cutPoint.CardHeight == 0 {
			cutPoint.CardHeight = outputCardHeight
		}
		cutPoints = append(cutPoints, cutPoint)
	}

	fmt.Printf("✅ 切分点计算完成，共 %d 个切分点\n", len(cutPoints))
	for i, cp := range cutPoints {
		fmt.Printf("📄 切分点 %d: Y=%d-%d, 高度=%d\n", i+1, cp.StartY, cp.EndY, cp.CardHeight)
	}

	return cutPoints, nil
}

// generateSuperLongHTML 生成超长图的HTML
func (p *SuperLongImageProcessor) generateSuperLongHTML(measurements []CardMeasurement) (string, int, error) {
	fmt.Printf("🎨 生成超长图HTML\n")

	var totalHeight int
	var htmlParts []string

	// HTML头部
	htmlParts = append(htmlParts, `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Super Long Image</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { 
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans CJK SC', 'Hiragino Sans GB', 'Microsoft YaHei'; 
            width: 1080px; 
            background: #ffffff; 
        }
        .card-section { width: 1080px; position: relative; }
        .first-card { height: 1440px; }
        .image-section { 
            width: 1080px; 
            height: 864px; 
            background-size: cover; 
            background-position: center; 
        }
        .title-section { 
            width: 1080px; 
            height: 576px; 
            background-color: #F5F5F5; 
            display: flex; 
            align-items: center; 
            justify-content: center; 
            padding: 0 60px; 
        }
        .title-text { 
            font-size: 36px; 
            font-weight: bold; 
            color: #333333; 
            text-align: center; 
            line-height: 1.4; 
        }
        .content-card { padding: 60px; }
        .element-subtitle { 
            font-size: 24px; 
            color: #4A4A4A; 
            margin: 25px 0; 
            border-bottom: 1px solid #E0E0E0; 
            padding-bottom: 25px; 
        }
        .element-body { 
            font-size: 18px; 
            color: #333333; 
            line-height: 1.8; 
            text-align: justify; 
            margin-bottom: 20px; 
        }
        .element-list { 
            font-size: 18px; 
            color: #333333; 
            list-style: none; 
            padding-left: 30px; 
            margin-bottom: 20px; 
        }
        .list-item { 
            margin-bottom: 15px; 
            position: relative; 
        }
        .list-item:before { 
            content: "・"; 
            color: #FF6B35; 
            position: absolute; 
            left: -30px; 
            font-weight: bold; 
        }
        .element-quote { 
            font-size: 18px; 
            color: #2D3748; 
            font-style: italic; 
            background-color: #F0F7FF; 
            border-left: 5px solid #1E88E5; 
            padding: 20px; 
            margin: 30px 0; 
        }
    </style>
</head>
<body>`)

	// 生成每张卡片的HTML
	for _, measurement := range measurements {
		if measurement.IsFirstCard {
			// 第一张卡片
			cardHTML := fmt.Sprintf(`
    <div class="card-section first-card">
        <div class="image-section" style="background-image: url('%s');"></div>
        <div class="title-section">
            <div class="title-text">%s</div>
        </div>
    </div>`, measurement.ImageURL, measurement.TitleContent)
			htmlParts = append(htmlParts, cardHTML)
			totalHeight += 1440
		} else {
			// 内容卡片
			cardHTML := fmt.Sprintf(`
    <div class="card-section content-card" style="min-height: %dpx;">`, measurement.Height)

			for _, element := range measurement.Elements {
				switch element.Type {
				case pagination.ElementTypeSubtitle:
					cardHTML += fmt.Sprintf(`
        <div class="element-subtitle">%s</div>`, element.Content)
				case pagination.ElementTypeBody:
					cardHTML += fmt.Sprintf(`
        <div class="element-body">%s</div>`, element.Content)
				case pagination.ElementTypeList:
					fmt.Printf("🔍 调试：超长图处理list元素，内容类型: %T\n", element.Content)
					cardHTML += `
        <ul class="element-list">`
					if items, ok := element.Content.([]string); ok {
						fmt.Printf("🔍 调试：超长图list转换成功([]string)，项目数: %d\n", len(items))
						for i, item := range items {
							fmt.Printf("🔍 调试：超长图list项 %d: %s\n", i, item)
							cardHTML += fmt.Sprintf(`
            <li class="list-item">%s</li>`, item)
						}
					} else if items, ok := element.Content.([]interface{}); ok {
						fmt.Printf("🔍 调试：超长图list内容为[]interface{}，原始长度: %d\n", len(items))
						for i, item := range items {
							converted := fmt.Sprintf("%v", item)
							fmt.Printf("🔍 调试：超长图list项 %d 转换: %v -> %s\n", i, item, converted)
							cardHTML += fmt.Sprintf(`
            <li class="list-item">%s</li>`, converted)
						}
					} else {
						fmt.Printf("🔍 调试：超长图list内容类型不匹配: %T，内容: %v\n", element.Content, element.Content)
					}
					cardHTML += `
        </ul>`
				case pagination.ElementTypeQuote:
					cardHTML += fmt.Sprintf(`
        <blockquote class="element-quote">%s</blockquote>`, element.Content)
				default:
					cardHTML += fmt.Sprintf(`
        <div class="element-body">%s</div>`, element.Content)
				}
			}

			cardHTML += `
    </div>`
			htmlParts = append(htmlParts, cardHTML)
			totalHeight += measurement.Height
		}
	}

	// HTML尾部
	htmlParts = append(htmlParts, `
</body>
</html>`)

	fullHTML := ""
	for _, part := range htmlParts {
		fullHTML += part
	}

	fmt.Printf("✅ 超长图HTML生成完成，总高度: %d px\n", totalHeight)
	return fullHTML, totalHeight, nil
}

// renderSuperLongImage 渲染超长图
func (p *SuperLongImageProcessor) renderSuperLongImage(htmlContent string, totalHeight int) ([]byte, error) {
	fmt.Printf("🖥️ 开始渲染超长图，高度: %d px\n", totalHeight)

	// 保存HTML到当前目录（而不是/tmp）
	tmpFile := fmt.Sprintf("super_long_%d.html", time.Now().Unix())
	if err := os.WriteFile(tmpFile, []byte(htmlContent), 0644); err != nil {
		return nil, fmt.Errorf("写入临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile)

	fmt.Printf("📁 HTML文件已保存: %s\n", tmpFile)

	// 获取绝对路径
	absPath, err := filepath.Abs(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("获取绝对路径失败: %v", err)
	}

	fmt.Printf("📍 HTML文件绝对路径: %s\n", absPath)

	// 创建Chrome上下文，设置超长视口和优化参数
	// 确保窗口高度足够容纳全部内容，添加额外缓冲区
	windowHeight := totalHeight + 200 // 添加200px缓冲区
	fmt.Printf("🔧 设置Chrome窗口尺寸: 1080x%d (内容高度: %d + 缓冲区: 200)\n", windowHeight, totalHeight)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("disable-features", "VizDisplayCompositor,Translate,BackForwardCache,AcceptCHFrame,MediaRouter,OptimizationHints,AudioServiceOutOfProcess"),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-ipc-flooding-protection", true),
		chromedp.Flag("window-size", fmt.Sprintf("1080,%d", windowHeight)),
		chromedp.Flag("max_old_space_size", "16384"), // 进一步增加内存限制到16GB
		chromedp.Flag("memory-pressure-off", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-logging", true),                                    // 禁用日志
		chromedp.Flag("disable-breakpad", true),                                   // 禁用崩溃报告
		chromedp.Flag("disable-plugins", true),                                    // 禁用插件
		chromedp.Flag("disable-component-extensions-with-background-pages", true), // 禁用后台扩展
		chromedp.Flag("disable-client-side-phishing-detection", true),             // 禁用钓鱼检测
		chromedp.Flag("disable-hang-monitor", true),                               // 禁用挂起监控
		chromedp.Flag("disable-prompt-on-repost", true),                           // 禁用重发提示
		chromedp.Flag("disable-domain-reliability", true),                         // 禁用域名可靠性
		chromedp.Flag("disable-features", "TranslateUI"),                          // 禁用翻译UI
		chromedp.Flag("disable-blink-features", "AutomationControlled"),           // 避免检测
		chromedp.Flag("disable-field-trial-config", true),                         // 禁用字段试验配置
		chromedp.Flag("disable-background-mode", true),                            // 禁用后台模式
		chromedp.Flag("disable-software-rasterizer", true),                        // 禁用软件光栅化
		chromedp.Flag("disable-canvas-aa", true),                                  // 禁用canvas抗锯齿
		chromedp.Flag("disable-2d-canvas-clip-aa", true),                          // 禁用2D canvas剪裁抗锯齿
		chromedp.Flag("disable-gl-drawing-for-tests", true),                       // 禁用OpenGL绘图
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	// 设置总体超时时间（根据图片高度动态调整）
	// 针对当前1840px的情况，直接设置充足的超时时间
	renderTimeout := 180 * time.Second // 基础超时增加到180秒
	if totalHeight > 2000 {
		renderTimeout = 240 * time.Second // 较大图片240秒（4分钟）
	}
	if totalHeight > 3000 {
		renderTimeout = 300 * time.Second // 超大图片300秒（5分钟）
	}

	fmt.Printf("⏱️ 根据内容高度 %dpx 设置渲染超时: %v\n", totalHeight, renderTimeout)

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 创建带超时的上下文
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, renderTimeout)
	defer timeoutCancel()

	fmt.Printf("⏱️ 渲染超时设置: %v\n", renderTimeout)

	var imageData []byte

	// 渲染截图
	fmt.Printf("🌐 正在加载HTML文件...\n")
	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate("file://"+absPath),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Printf("⏳ 等待页面加载完成...\n")
			return nil
		}),
		chromedp.WaitVisible("body"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Printf("🖼️ 设置视口大小: 1080x%d (匹配窗口尺寸)\n", windowHeight)
			return chromedp.EmulateViewport(1080, int64(windowHeight)).Do(ctx)
		}),
		chromedp.Sleep(500*time.Millisecond), // 进一步减少等待时间
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Printf("📸 开始截图...\n")
			return nil
		}),
		chromedp.CaptureScreenshot(&imageData),
	)

	if err != nil {
		// 根据错误类型提供更详细的信息
		if err == context.DeadlineExceeded {
			return nil, fmt.Errorf("chrome渲染超时 (超时时间: %v, 内容高度: %dpx, 窗口高度: %dpx): %v", renderTimeout, totalHeight, windowHeight, err)
		}
		return nil, fmt.Errorf("chrome渲染失败 (内容高度: %dpx, 窗口高度: %dpx): %v", totalHeight, windowHeight, err)
	}

	if len(imageData) == 0 {
		return nil, fmt.Errorf("chrome渲染成功但图片数据为空")
	}

	fmt.Printf("✅ 超长图渲染完成，图片大小: %d bytes (%.2f MB)\n", len(imageData), float64(len(imageData))/1024/1024)
	return imageData, nil
}

// cropImagesBySubPoints 按切分点截取图片
func (p *SuperLongImageProcessor) cropImagesBySubPoints(
	superLongImageData []byte,
	cutPoints []CutPoint,
	bookID uint,
) ([]*RenderedCard, error) {
	fmt.Printf("✂️ 开始按切分点截取图片，共 %d 个切分点\n", len(cutPoints))

	// 实现图片截取逻辑
	fmt.Printf("🖼️ 开始解析超长图片数据...\n")

	var renderedCards []*RenderedCard

	for i, cutPoint := range cutPoints {
		sortOrder := i + 1
		cardID := bookID*1000 + uint(sortOrder)

		fmt.Printf("✂️ 截取第 %d 张卡片，起始Y=%d，结束Y=%d，卡片高度=%d\n", sortOrder, cutPoint.StartY, cutPoint.EndY, cutPoint.CardHeight)

		// 真正的图片截取过程
		croppedImageData, err := p.cropImageRegion(superLongImageData, cutPoint)
		if err != nil {
			fmt.Printf("❌ 截取第 %d 张图片失败: %v，使用原图\n", sortOrder, err)
			croppedImageData = superLongImageData // 降级到原图
		}

		// 保存截取的图片
		imageURL, err := p.saveCroppedImage(croppedImageData, cardID)
		if err != nil {
			fmt.Printf("⚠️ 保存第 %d 张截取图片失败: %v\n", sortOrder, err)
			continue
		}

		renderedCard := &RenderedCard{
			CardID:    cardID,
			ImageURL:  imageURL,
			Width:     1080,
			Height:    1440,
			SortOrder: sortOrder,
		}

		renderedCards = append(renderedCards, renderedCard)
		fmt.Printf("✅ 第 %d 张图片截取完成: %s\n", sortOrder, imageURL)
	}

	fmt.Printf("✅ 所有图片截取完成，共 %d 张\n", len(renderedCards))
	return renderedCards, nil
}

// 辅助函数

func (p *SuperLongImageProcessor) extractTitleContent(elements []pagination.Element) string {
	for _, element := range elements {
		if element.Type == pagination.ElementTypeTitle {
			if content, ok := element.Content.(string); ok {
				return content
			}
		}
	}
	return "默认标题"
}

func (p *SuperLongImageProcessor) filterOutUsedTitle(elements []pagination.Element) []pagination.Element {
	var remaining []pagination.Element
	titleUsed := false

	for _, element := range elements {
		if element.Type == pagination.ElementTypeTitle && !titleUsed {
			titleUsed = true
			continue
		}
		remaining = append(remaining, element)
	}

	return remaining
}

func (p *SuperLongImageProcessor) measureSingleElementHeight(element pagination.Element) (int, error) {
	// 简化的高度估算，实际应该使用无头浏览器测量
	switch element.Type {
	case pagination.ElementTypeSubtitle:
		return 80, nil
	case pagination.ElementTypeBody:
		contentLen := len(fmt.Sprintf("%v", element.Content))
		lines := (contentLen / 50) + 1 // 估算行数
		return lines * 32, nil         // 每行约32px
	case pagination.ElementTypeList:
		if items, ok := element.Content.([]string); ok {
			return len(items) * 40, nil // 每项约40px
		}
		return 80, nil
	case pagination.ElementTypeQuote:
		contentLen := len(fmt.Sprintf("%v", element.Content))
		lines := (contentLen / 45) + 1 // 引用文字略宽
		return (lines * 30) + 80, nil  // 内容 + 边距
	default:
		return 100, nil
	}
}

// cropImageRegion 从图片数据中截取指定区域
func (p *SuperLongImageProcessor) cropImageRegion(imageData []byte, cutPoint CutPoint) ([]byte, error) {
	fmt.Printf("🔧 解析图片数据，大小: %d bytes\n", len(imageData))

	// 解析图片数据
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, fmt.Errorf("解析图片失败: %v", err)
	}

	bounds := img.Bounds()
	fmt.Printf("📐 原图尺寸: %dx%d\n", bounds.Dx(), bounds.Dy())

	// 计算截取区域
	x := 0 // 从左边开始
	y := cutPoint.StartY
	width := 1080                             // 固定宽度
	height := cutPoint.EndY - cutPoint.StartY // 计算实际高度

	// 边界检查
	if y < 0 {
		y = 0
	}
	if y+height > bounds.Dy() {
		height = bounds.Dy() - y
		fmt.Printf("⚠️ 截取高度调整为: %d (原始: %d)\n", height, cutPoint.EndY-cutPoint.StartY)
	}

	if height <= 0 {
		return nil, fmt.Errorf("无效的截取区域: y=%d, height=%d, 图片高度=%d", y, height, bounds.Dy())
	}

	fmt.Printf("✂️ 截取区域: x=%d, y=%d, width=%d, height=%d\n", x, y, width, height)

	// 执行截取
	croppedImg := image.NewRGBA(image.Rect(0, 0, width, height))

	// 复制像素
	for py := 0; py < height; py++ {
		for px := 0; px < width; px++ {
			srcX := x + px
			srcY := y + py
			if srcX < bounds.Max.X && srcY < bounds.Max.Y {
				croppedImg.Set(px, py, img.At(srcX, srcY))
			}
		}
	}

	fmt.Printf("✅ 图片截取完成，新尺寸: %dx%d\n", croppedImg.Bounds().Dx(), croppedImg.Bounds().Dy())

	// 编码为PNG
	var buf bytes.Buffer
	if err := png.Encode(&buf, croppedImg); err != nil {
		return nil, fmt.Errorf("编码截取图片失败: %v", err)
	}

	fmt.Printf("💾 截取图片编码完成，大小: %d bytes\n", buf.Len())
	return buf.Bytes(), nil
}

func (p *SuperLongImageProcessor) saveCroppedImage(imageData []byte, cardID uint) (string, error) {
	// 使用工具函数获取正确的图片保存路径
	cardDir := util.GetCardImagePath(cardID)

	// 确保目录存在
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %v", err)
	}

	// 生成文件名
	filename := fmt.Sprintf("card_%d.png", cardID)
	filepath := filepath.Join(cardDir, filename)

	// 写入文件
	if err := os.WriteFile(filepath, imageData, 0644); err != nil {
		return "", fmt.Errorf("写入图片文件失败: %v", err)
	}

	// 返回图片URL
	return util.GetCardImageURL(cardID, filename), nil
}
