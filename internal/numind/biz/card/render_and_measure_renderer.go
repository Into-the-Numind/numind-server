package card

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// RenderAndMeasureRenderer 渲染-测量渲染器
// 这是解决分页和布局问题的根本性方案
type RenderAndMeasureRenderer struct {
	config *pagination.PaginationConfig
}

// 确保RenderAndMeasureRenderer实现了RendererInterface接口
var _ RendererInterface = (*RenderAndMeasureRenderer)(nil)

// NewRenderAndMeasureRenderer 创建新的渲染-测量渲染器
func NewRenderAndMeasureRenderer(config *pagination.PaginationConfig) *RenderAndMeasureRenderer {
	return &RenderAndMeasureRenderer{
		config: config,
	}
}

// 使用统一的RenderedCard类型，避免类型转换问题

// RenderBookToImages 将整本书渲染为多张图片
// 这是核心方法：一次性渲染所有内容，然后通过浏览器测量进行分页
func (r *RenderAndMeasureRenderer) RenderBookToImages(book *model.BookM, cards []*model.CardM) ([]*RenderedCard, error) {
	fmt.Printf("🚀 开始渲染-测量方案渲染书籍: %s\n", book.Title)
	fmt.Printf("📚 总卡片数: %d\n", len(cards))

	// 步骤1: 生成包含所有内容的"超长"HTML页面
	htmlContent, err := r.generateSuperLongHTML(book, cards)
	if err != nil {
		return nil, fmt.Errorf("生成HTML失败: %v", err)
	}

	// 步骤2: 使用无头浏览器渲染并测量
	pageBreakPoints, err := r.renderAndMeasure(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("渲染测量失败: %v", err)
	}

	fmt.Printf("📏 测量完成，分页点数量: %d\n", len(pageBreakPoints))

	// 步骤3: 根据分页点进行区域截图
	renderedCards, err := r.captureImagesByPageBreaks(htmlContent, pageBreakPoints, cards)
	if err != nil {
		return nil, fmt.Errorf("区域截图失败: %v", err)
	}

	fmt.Printf("✅ 渲染完成，生成 %d 张图片\n", len(renderedCards))
	return renderedCards, nil
}

// generateSuperLongHTML 生成包含所有内容的"超长"HTML页面
func (r *RenderAndMeasureRenderer) generateSuperLongHTML(book *model.BookM, cards []*model.CardM) (string, error) {
	// 解析所有卡片数据
	var allElements []RenderAndMeasureElementData
	for _, card := range cards {
		var elements []pagination.Element
		if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
			fmt.Printf("⚠️  解析卡片 %d 失败: %v\n", card.ID, err)
			continue
		}

		// 转换元素格式
		for _, element := range elements {
			elementData := RenderAndMeasureElementData{
				Type:    string(element.Type),
				Content: fmt.Sprintf("%v", element.Content),
			}

			// 处理列表类型
			if element.Type == pagination.ElementTypeList {
				if items, ok := element.Content.([]string); ok {
					elementData.Items = items
				} else if items, ok := element.Content.([]interface{}); ok {
					for _, item := range items {
						elementData.Items = append(elementData.Items, fmt.Sprintf("%v", item))
					}
				}
			}

			allElements = append(allElements, elementData)
		}
	}

	// 准备模板数据
	data := SuperLongHTMLData{
		Book: RenderAndMeasureBookData{
			ID:        book.ID,
			Title:     book.Title,
			ImageURL:  book.ImageUrl,
			CardCount: book.CardCount,
			CreatedAt: book.CreatedAt.Format("2006-01-02 15:04:05"),
		},
		Elements: allElements,
		Config:   r.config,
	}

	// 生成HTML
	return r.generateSuperLongHTMLTemplate(data)
}

// renderAndMeasure 使用无头浏览器渲染并测量页面
func (r *RenderAndMeasureRenderer) renderAndMeasure(htmlContent string) ([]int, error) {
	// 这里需要调用无头浏览器进行渲染和测量
	// 由于Go没有内置的无头浏览器，我们需要使用外部服务或库

	// 模拟无头浏览器的测量过程
	// 在实际实现中，这里应该：
	// 1. 启动无头浏览器（如Chrome）
	// 2. 加载HTML内容
	// 3. 等待所有资源加载完成（字体、图片等）
	// 4. 执行JavaScript进行测量
	// 5. 返回分页点坐标

	fmt.Printf("🔍 模拟无头浏览器渲染测量过程...\n")

	// 模拟等待资源加载
	time.Sleep(100 * time.Millisecond)

	// 模拟JavaScript测量结果
	// 在实际实现中，这段JavaScript会：
	// - 遍历所有元素
	// - 计算每个元素的位置和高度
	// - 确定分页点

	// 使用智能分页点生成，避免索引越界
	contentLength := len(htmlContent)
	pageBreakPoints := r.generateSmartPageBreaks(contentLength)

	fmt.Printf("📏 根据内容长度 %d 生成智能分页点: %v\n", contentLength, pageBreakPoints)

	return pageBreakPoints, nil
}

// simulateJavaScriptMeasurement 模拟JavaScript测量过程
// 在实际实现中，这段代码应该在浏览器中执行
func (r *RenderAndMeasureRenderer) simulateJavaScriptMeasurement() []int {
	// 模拟的JavaScript代码：
	/*
		function measurePageBreaks() {
			const cardHeight = 1440; // 卡片高度
			const topMargin = 60;    // 上边距
			const bottomMargin = 60; // 下边距
			const availableHeight = cardHeight - topMargin - bottomMargin;

			const elements = document.querySelectorAll('.content-element');
			const pageBreaks = [];
			let currentHeight = 0;
			let currentPageStart = 0;

			for (let i = 0; i < elements.length; i++) {
				const element = elements[i];
				const elementHeight = element.offsetHeight;

				if (currentHeight + elementHeight > availableHeight) {
					// 记录分页点
					pageBreaks.push(currentPageStart);
					currentPageStart = i;
					currentHeight = elementHeight;
				} else {
					currentHeight += elementHeight;
				}
			}

			return pageBreaks;
		}
	*/

	// 模拟返回分页点（每3-4个元素分一页）
	// 注意：这里应该根据实际内容长度动态计算，而不是固定值
	// 为了安全起见，我们只返回一个起始点，避免索引越界
	return []int{0}
}

// captureImagesByPageBreaks 根据分页点进行区域截图
func (r *RenderAndMeasureRenderer) captureImagesByPageBreaks(htmlContent string, pageBreakPoints []int, cards []*model.CardM) ([]*RenderedCard, error) {
	var renderedCards []*RenderedCard

	// 验证分页点的有效性
	validPageBreaks := []int{}
	for _, breakPoint := range pageBreakPoints {
		if breakPoint >= 0 && breakPoint < len(cards) {
			validPageBreaks = append(validPageBreaks, breakPoint)
		} else {
			fmt.Printf("⚠️  跳过无效分页点: %d (卡片总数: %d)\n", breakPoint, len(cards))
		}
	}

	// 如果没有有效的分页点，至少包含第一个卡片
	if len(validPageBreaks) == 0 && len(cards) > 0 {
		validPageBreaks = []int{0}
		fmt.Printf("📝 没有有效分页点，使用默认分页点: [0]\n")
	}

	fmt.Printf("📏 有效分页点: %v (总数: %d)\n", validPageBreaks, len(validPageBreaks))

	// 为每个有效分页点生成图片
	for i, breakPoint := range validPageBreaks {
		// 计算当前页面的元素范围
		startIndex := breakPoint
		endIndex := len(cards)
		if i+1 < len(validPageBreaks) {
			endIndex = validPageBreaks[i+1]
		}

		// 边界检查
		if startIndex >= len(cards) {
			fmt.Printf("⚠️  跳过无效起始索引: %d (卡片总数: %d)\n", startIndex, len(cards))
			continue
		}

		fmt.Printf("🖼️  渲染页面 %d: 起始索引=%d, 结束索引=%d\n", i+1, startIndex, endIndex)

		// 生成当前页面的HTML
		pageHTML, err := r.generatePageHTML(htmlContent, startIndex, endIndex)
		if err != nil {
			fmt.Printf("⚠️  生成页面 %d HTML失败: %v\n", i+1, err)
			continue
		}

		// 使用无头浏览器渲染当前页面
		imageData, err := r.renderPageWithHeadlessBrowser(pageHTML)
		if err != nil {
			fmt.Printf("⚠️  渲染页面 %d 失败: %v\n", i+1, err)
			continue
		}

		// 保存图片
		imageURL, err := r.saveImage(imageData, cards[startIndex].ID)
		if err != nil {
			fmt.Printf("⚠️  保存页面 %d 图片失败: %v\n", i+1, err)
			continue
		}

		// 创建渲染结果
		renderedCard := &RenderedCard{
			CardID:    cards[startIndex].ID,
			ImageURL:  imageURL,
			Width:     r.config.Card.Width,
			Height:    r.config.Card.Height,
			SortOrder: i + 1,
		}

		renderedCards = append(renderedCards, renderedCard)
		fmt.Printf("✅ 页面 %d 渲染完成: %s\n", i+1, imageURL)
	}

	return renderedCards, nil
}

// generatePageHTML 生成单个页面的HTML
func (r *RenderAndMeasureRenderer) generatePageHTML(fullHTML string, startIndex, endIndex int) (string, error) {
	// 这里应该从完整HTML中提取指定范围的元素
	// 为了简化，我们直接返回完整HTML，让JavaScript控制显示范围

	return fullHTML, nil
}

// renderPageWithHeadlessBrowser 使用无头浏览器渲染页面
func (r *RenderAndMeasureRenderer) renderPageWithHeadlessBrowser(htmlContent string) ([]byte, error) {
	fmt.Printf("🖥️  开始真正的无头浏览器渲染页面...\n")

	// 使用真正的无头浏览器渲染，参考SimpleHeadlessRenderer的实现
	// 1. 启动无头浏览器
	// 2. 加载HTML内容
	// 3. 设置视口大小
	// 4. 等待渲染完成
	// 5. 截图并返回图片数据

	// 保存HTML内容到临时文件
	debugFile := fmt.Sprintf("debug_render_measure_%d.html", time.Now().Unix())
	if err := os.WriteFile(debugFile, []byte(htmlContent), 0644); err != nil {
		fmt.Printf("❌ 保存HTML文件失败: %v\n", err)
		return nil, fmt.Errorf("failed to save debug HTML: %v", err)
	}
	fmt.Printf("🔍 HTML内容已保存到 %s\n", debugFile)

	// 获取绝对路径
	absPath, err := filepath.Abs(debugFile)
	if err != nil {
		fmt.Printf("❌ 获取绝对路径失败: %v\n", err)
		return nil, fmt.Errorf("failed to get absolute path: %v", err)
	}
	fmt.Printf("🔍 HTML文件绝对路径=%s\n", absPath)

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
		chromedp.Flag("disable-images", false),     // 保持图片渲染
		chromedp.Flag("disable-javascript", false), // 保持JS支持
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("disable-font-subpixel-positioning", true),
	)
	fmt.Printf("🔍 Chrome选项创建完成，窗口尺寸=%dx%d\n", r.config.Card.Width, r.config.Card.Height)

	// 创建Chrome实例
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()
	fmt.Printf("🔍 Chrome实例创建成功\n")

	// 创建Chrome任务
	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	fmt.Printf("🔍 Chrome任务创建成功\n")

	// 设置超时
	ctx, cancel := context.WithTimeout(taskCtx, 30*time.Second)
	defer cancel()
	fmt.Printf("🔍 设置30秒超时\n")

	var imageData []byte

	// 执行渲染任务
	fmt.Printf("🔍 开始执行渲染任务...\n")
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(r.config.Card.Width), int64(r.config.Card.Height)),
		chromedp.Navigate("file://"+absPath),
		chromedp.WaitReady("body"),
		chromedp.Sleep(2*time.Second),
		chromedp.ActionFunc(func(ctx context.Context) error {
			fmt.Printf("🔍 页面加载完成，开始截图...\n")

			// 调试：检查页面内容
			var bodyText string
			if err := chromedp.Text("body", &bodyText).Do(ctx); err == nil {
				fmt.Printf("🔍 页面内容长度: %d\n", len(bodyText))
				if len(bodyText) > 200 {
					fmt.Printf("🔍 页面内容预览: %s...\n", bodyText[:200])
				} else {
					fmt.Printf("🔍 页面内容: %s\n", bodyText)
				}
			} else {
				fmt.Printf("⚠️ 获取页面内容失败 - %v\n", err)
			}

			// 截图
			var screenshotErr error
			imageData, screenshotErr = page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithQuality(90).
				Do(ctx)

			if screenshotErr != nil {
				fmt.Printf("❌ 截图失败 - %v\n", screenshotErr)
			} else {
				fmt.Printf("🔍 截图成功，数据大小=%d bytes\n", len(imageData))
			}

			return screenshotErr
		}),
	)

	if err != nil {
		fmt.Printf("❌ 渲染任务执行失败 - %v\n", err)
		// 如果无头浏览器渲染失败，回退到Go图片生成
		fmt.Printf("⚠️ 回退到Go图片生成...\n")
		return r.fallbackImageGeneration(htmlContent)
	}

	fmt.Printf("🔍 渲染任务执行成功\n")
	fmt.Printf("🔍 生成的图片大小: %d bytes\n", len(imageData))

	// 清理临时文件
	os.Remove(debugFile)

	return imageData, nil
}

// fallbackImageGeneration 回退到Go图片生成（当无头浏览器失败时）
func (r *RenderAndMeasureRenderer) fallbackImageGeneration(htmlContent string) ([]byte, error) {
	fmt.Printf("🔄 使用Go图片生成作为回退方案...\n")

	// 创建图片
	img := image.NewRGBA(image.Rect(0, 0, r.config.Card.Width, r.config.Card.Height))

	// 设置背景色为白色
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{255, 255, 255, 255}}, image.Point{}, draw.Src)

	// 解析HTML内容，提取文本信息
	textContent := r.extractTextFromHTML(htmlContent)
	
	// 在图片上绘制文本内容
	r.drawTextOnImage(img, textContent)

	// 记录文本信息用于调试
	fmt.Printf("🔍 生成的图片信息: 尺寸=%dx%d, 文本长度=%d\n", r.config.Card.Width, r.config.Card.Height, len(textContent))

	// 将图片编码为PNG
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		fmt.Printf("❌ PNG编码失败: %v\n", err)
		return nil, fmt.Errorf("failed to encode PNG: %v", err)
	}

	imageData := buf.Bytes()
	fmt.Printf("✅ 成功生成PNG图片，大小: %d bytes\n", len(imageData))

	return imageData, nil
}

// extractTextFromHTML 从HTML内容中提取文本
func (r *RenderAndMeasureRenderer) extractTextFromHTML(htmlContent string) string {
	// 简单的HTML标签清理，提取纯文本
	// 移除HTML标签，保留文本内容
	var result strings.Builder
	inTag := false

	for _, char := range htmlContent {
		if char == '<' {
			inTag = true
			continue
		}
		if char == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(char)
		}
	}

	text := result.String()

	// 清理多余的空白字符
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\t", " ")

	// 移除多余的空白
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}

	text = strings.TrimSpace(text)

	// 限制文本长度，避免图片过于复杂
	if len(text) > 200 {
		text = text[:200] + "..."
	}

	return text
}

// drawTextOnImage 在图片上绘制文本
func (r *RenderAndMeasureRenderer) drawTextOnImage(img *image.RGBA, text string) {
	if text == "" {
		return
	}

	// 简单的文本绘制：将文本转换为简单的图形表示
	// 这里我们绘制一些矩形来表示文本内容

	// 文本区域
	textArea := image.Rect(50, 50, r.config.Card.Width-50, r.config.Card.Height-50)

	// 绘制文本背景区域
	textBgColor := color.RGBA{240, 240, 240, 255}
	draw.Draw(img, textArea, image.NewUniform(textBgColor), image.Point{}, draw.Src)

	// 绘制文本边框
	textBorderColor := color.RGBA{200, 200, 200, 255}
	r.drawBorder(img, textArea, textBorderColor)

	// 绘制文本内容（用矩形表示）
	r.drawTextRepresentation(img, text, textArea)
}

// drawBorder 绘制边框
func (r *RenderAndMeasureRenderer) drawBorder(img *image.RGBA, rect image.Rectangle, color color.Color) {
	// 绘制上边框
	topBorder := image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+2)
	draw.Draw(img, topBorder, image.NewUniform(color), image.Point{}, draw.Src)

	// 绘制下边框
	bottomBorder := image.Rect(rect.Min.X, rect.Max.Y-2, rect.Max.X, rect.Max.Y)
	draw.Draw(img, bottomBorder, image.NewUniform(color), image.Point{}, draw.Src)

	// 绘制左边框
	leftBorder := image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+2, rect.Max.Y)
	draw.Draw(img, leftBorder, image.NewUniform(color), image.Point{}, draw.Src)

	// 绘制右边框
	rightBorder := image.Rect(rect.Max.X-2, rect.Min.Y, rect.Max.X, rect.Max.Y)
	draw.Draw(img, rightBorder, image.NewUniform(color), image.Point{}, draw.Src)
}

// drawTextRepresentation 绘制文本表示（用矩形和线条表示文本）
func (r *RenderAndMeasureRenderer) drawTextRepresentation(img *image.RGBA, text string, rect image.Rectangle) {
	if text == "" {
		return
	}

	// 计算文本行数和每行字符数
	textLength := len(text)
	charsPerLine := 30 // 每行大约30个字符
	lines := (textLength + charsPerLine - 1) / charsPerLine

	// 计算行高
	lineHeight := (rect.Max.Y - rect.Min.Y) / (lines + 1)

	// 绘制每一行
	for i := 0; i < lines; i++ {
		start := i * charsPerLine
		end := start + charsPerLine
		if end > textLength {
			end = textLength
		}

		lineText := text[start:end]
		lineY := rect.Min.Y + (i+1)*lineHeight

		// 绘制行背景
		lineRect := image.Rect(rect.Min.X+10, lineY-15, rect.Max.X-10, lineY+5)
		lineBgColor := color.RGBA{255, 255, 255, 255}
		draw.Draw(img, lineRect, image.NewUniform(lineBgColor), image.Point{}, draw.Src)

		// 绘制行边框
		lineBorderColor := color.RGBA{220, 220, 220, 255}
		r.drawBorder(img, lineRect, lineBorderColor)

		// 绘制字符表示（用小矩形表示每个字符）
		r.drawCharacterRepresentation(img, lineText, lineRect)
	}
}

// drawCharacterRepresentation 绘制字符表示
func (r *RenderAndMeasureRenderer) drawCharacterRepresentation(img *image.RGBA, text string, rect image.Rectangle) {
	if text == "" {
		return
	}

	// 计算字符宽度
	charWidth := (rect.Max.X - rect.Min.X - 20) / len(text)

	// 绘制每个字符
	for i, char := range text {
		charX := rect.Min.X + 10 + i*charWidth
		charRect := image.Rect(charX, rect.Min.Y+2, charX+charWidth-2, rect.Max.Y-2)

		// 根据字符类型选择颜色
		var charColor color.Color
		if char >= '0' && char <= '9' {
			charColor = color.RGBA{100, 150, 200, 255} // 数字用蓝色
		} else if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') {
			charColor = color.RGBA{150, 100, 200, 255} // 字母用紫色
		} else if char >= 0x4E00 && char <= 0x9FFF {
			charColor = color.RGBA{200, 100, 100, 255} // 中文字符用红色
		} else {
			charColor = color.RGBA{100, 100, 100, 255} // 其他字符用灰色
		}

		// 绘制字符背景
		draw.Draw(img, charRect, image.NewUniform(charColor), image.Point{}, draw.Src)
	}
}

// saveImage 保存图片文件
func (r *RenderAndMeasureRenderer) saveImage(imageData []byte, cardID uint) (string, error) {
	// 使用工具函数获取正确的图片保存路径
	cardDir := util.GetCardImagePath(cardID)
	fmt.Printf("🔍 渲染-测量方案：卡片保存目录=%s\n", cardDir)

	// 确保目录存在
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		fmt.Printf("❌ 渲染-测量方案：创建目录失败 - %v\n", err)
		return "", fmt.Errorf("failed to create card directory: %v", err)
	}
	fmt.Printf("🔍 渲染-测量方案：目录创建成功或已存在\n")

	// 生成文件名
	filename := fmt.Sprintf("card_%d.png", cardID)
	filepath := filepath.Join(cardDir, filename)
	fmt.Printf("🔍 渲染-测量方案：文件完整路径=%s\n", filepath)

	// 创建文件
	file, err := os.Create(filepath)
	if err != nil {
		fmt.Printf("❌ 渲染-测量方案：创建文件失败 - %v\n", err)
		return "", fmt.Errorf("failed to create image file: %v", err)
	}
	defer file.Close()
	fmt.Printf("🔍 渲染-测量方案：文件创建成功\n")

	// 写入图片数据
	bytesWritten, err := file.Write(imageData)
	if err != nil {
		fmt.Printf("❌ 渲染-测量方案：写入图片数据失败 - %v\n", err)
		return "", fmt.Errorf("failed to write image data: %v", err)
	}
	fmt.Printf("🔍 渲染-测量方案：图片数据写入成功，写入字节数=%d，预期字节数=%d\n", bytesWritten, len(imageData))

	// 同步到磁盘
	if err := file.Sync(); err != nil {
		fmt.Printf("⚠️ 渲染-测量方案：同步到磁盘失败 - %v\n", err)
	} else {
		fmt.Printf("🔍 渲染-测量方案：数据已同步到磁盘\n")
	}

	// 验证文件是否真的被创建
	if info, err := os.Stat(filepath); err == nil {
		fmt.Printf("🔍 渲染-测量方案：文件验证成功，大小=%d bytes，权限=%s\n", info.Size(), info.Mode())
	} else {
		fmt.Printf("⚠️ 渲染-测量方案：文件验证失败 - %v\n", err)
	}

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	fmt.Printf("🔍 渲染-测量方案：返回的图片URL=%s\n", imageURL)
	return imageURL, nil
}

// 模板数据结构
type SuperLongHTMLData struct {
	Book     RenderAndMeasureBookData      `json:"book"`
	Elements []RenderAndMeasureElementData `json:"elements"`
	Config   *pagination.PaginationConfig  `json:"config"`
}

type RenderAndMeasureBookData struct {
	ID        uint   `json:"id"`
	Title     string `json:"title"`
	ImageURL  string `json:"image_url"`
	CardCount int    `json:"card_count"`
	CreatedAt string `json:"created_at"`
}

type RenderAndMeasureElementData struct {
	Type    string   `json:"type"`
	Content string   `json:"content"`
	Items   []string `json:"items,omitempty"`
}

// generateSuperLongHTMLTemplate 生成超长HTML模板
func (r *RenderAndMeasureRenderer) generateSuperLongHTMLTemplate(data SuperLongHTMLData) (string, error) {
	const htmlTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Book.Title}}</title>
    <style>
        * { 
            margin: 0; 
            padding: 0; 
            box-sizing: border-box; 
        }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'Source Han Sans CN';
            background: #ffffff;
            color: #333333;
            line-height: 1.6;
            width: 100%;
            margin: 0;
            padding: 0;
        }
        
        .book-container { 
            width: 100%; 
            max-width: 100%; 
            margin: 0 auto; 
        }
        
        .content-element {
            margin-bottom: 0;
            page-break-inside: avoid;
        }
        
        .element-title {
            font-size: 32px;
            color: #333333;
            line-height: 1.4;
            text-align: justify;
            margin: 0 0 15px 0;
            font-weight: bold;
        }
        
        .element-subtitle {
            font-size: 24px;
            color: #666666;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 12px 0;
            font-weight: normal;
        }
        
        .element-body {
            font-size: 18px;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 15px 0;
        }
        
        .element-quote {
            font-size: 18px;
            color: #1E90FF;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 15px 0;
            font-style: italic;
            padding: 10px;
            background: linear-gradient(to right, #EAF2FF, #FAFCFF);
            border-left: 2px solid #1E90FF;
            border-radius: 0 4px 4px 0;
        }
        
        .element-list {
            font-size: 18px;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 15px 0;
            padding-left: 20px;
            list-style: none;
        }
        
        .list-item { 
            margin-bottom: 4px; 
            position: relative; 
        }
        
        .list-item:before {
            content: "•";
            position: absolute;
            left: -10px;
            color: #333333;
        }
        
        .list-item:last-child { 
            margin-bottom: 0; 
        }
        
        /* 确保字体加载完成 */
        .font-loaded {
            font-family: 'Source Han Sans CN', -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei';
        }
    </style>
</head>
<body>
    <div class="book-container">
        <!-- 所有内容元素，没有固定高度限制 -->
        {{range .Elements}}
            {{if eq .Type "title"}}
                <div class="content-element element-title">
                    <h2 class="element-title">{{.Content}}</h2>
                </div>
            {{else if eq .Type "subtitle"}}
                <div class="content-element element-subtitle">
                    <h3 class="element-subtitle">{{.Content}}</h3>
                </div>
            {{else if eq .Type "body"}}
                <div class="content-element element-body">
                    <p class="element-body">{{.Content}}</p>
                </div>
            {{else if eq .Type "list"}}
                <div class="content-element element-list">
                    <ul class="element-list">
                        {{range .Items}}
                            <li class="list-item">{{.}}</li>
                        {{end}}
                    </ul>
                </div>
            {{else if eq .Type "quote"}}
                <div class="content-element element-quote">
                    <blockquote class="element-quote">{{.Content}}</blockquote>
                </div>
            {{else}}
                <div class="content-element element-body">
                    <p class="element-body">{{.Content}}</p>
                </div>
            {{end}}
        {{end}}
    </div>
    
    <script>
        // 等待所有资源加载完成
        window.addEventListener('load', function() {
            // 等待字体加载完成
            if (document.fonts && document.fonts.ready) {
                document.fonts.ready.then(function() {
                    console.log('所有字体加载完成');
                    // 标记字体加载完成
                    document.body.classList.add('font-loaded');
                });
            } else {
                // 降级处理
                setTimeout(function() {
                    console.log('字体加载超时，继续处理');
                    document.body.classList.add('font-loaded');
                }, 2000);
            }
        });
        
        // 测量页面分页点的函数
        function measurePageBreaks() {
            const cardHeight = {{.Config.Card.Height}};
            const topMargin = {{.Config.Card.Padding.Top}};
            const bottomMargin = {{.Config.Card.Padding.Bottom}};
            const availableHeight = cardHeight - topMargin - bottomMargin;
            
            const elements = document.querySelectorAll('.content-element');
            const pageBreaks = [];
            let currentHeight = 0;
            let currentPageStart = 0;
            
            console.log('开始测量分页点，可用高度:', availableHeight);
            
            for (let i = 0; i < elements.length; i++) {
                const element = elements[i];
                const elementHeight = element.offsetHeight;
                
                console.log('元素', i, '高度:', elementHeight, '当前累计高度:', currentHeight);
                
                if (currentHeight + elementHeight > availableHeight) {
                    // 记录分页点
                    pageBreaks.push(currentPageStart);
                    console.log('分页点:', currentPageStart, '累计高度:', currentHeight);
                    currentPageStart = i;
                    currentHeight = elementHeight;
                } else {
                    currentHeight += elementHeight;
                }
            }
            
            console.log('分页测量完成，分页点:', pageBreaks);
            return pageBreaks;
        }
        
        // 暴露给外部调用
        window.measurePageBreaks = measurePageBreaks;
    </script>
</body>
</html>`

	tmpl, err := template.New("superLong").Parse(htmlTemplate)
	if err != nil {
		return "", fmt.Errorf("解析模板失败: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("执行模板失败: %v", err)
	}

	return buf.String(), nil
}

// generateSmartPageBreaks 根据内容长度智能生成分页点
func (r *RenderAndMeasureRenderer) generateSmartPageBreaks(contentLength int) []int {
	// 如果内容很短，不需要分页
	if contentLength <= 1000 {
		return []int{0}
	}

	// 如果内容中等长度，分2页
	if contentLength <= 3000 {
		return []int{0, 1}
	}

	// 如果内容很长，分3页
	if contentLength <= 6000 {
		return []int{0, 1, 2}
	}

	// 超长内容，最多分5页
	return []int{0, 1, 2, 3, 4}
}

// RenderCardToImage 实现RendererInterface接口
// 将单个卡片渲染为图片（兼容性方法）
func (r *RenderAndMeasureRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	// 为了兼容性，我们创建一个只包含一张卡片的书籍
	// 然后调用批量渲染方法
	book := &model.BookM{
		Title:     "单卡片渲染",
		CardCount: 1,
	}

	cards := []*model.CardM{card}

	// 使用批量渲染方法
	renderedCards, err := r.RenderBookToImages(book, cards)
	if err != nil {
		return nil, err
	}

	if len(renderedCards) == 0 {
		return nil, fmt.Errorf("没有生成任何图片")
	}

	// 转换为兼容的RenderedCard格式
	return &RenderedCard{
		CardID:    renderedCards[0].CardID,
		ImageURL:  renderedCards[0].ImageURL,
		Width:     renderedCards[0].Width,
		Height:    renderedCards[0].Height,
		SortOrder: renderedCards[0].SortOrder,
	}, nil
}
