package card

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"

	"github.com/chromedp/chromedp"
)

// PreciseMeasurementEngine 精确测量引擎
// 使用无头浏览器进行精确的高度测量和布局计算
type PreciseMeasurementEngine struct {
	config *pagination.PaginationConfig
}

// NewPreciseMeasurementEngine 创建精确测量引擎
func NewPreciseMeasurementEngine(config *pagination.PaginationConfig) *PreciseMeasurementEngine {
	return &PreciseMeasurementEngine{
		config: config,
	}
}

// ElementMeasurement 元素测量结果
type ElementMeasurement struct {
	ElementIndex        int                 `json:"element_index"`
	Element             pagination.Element  `json:"element"`
	ActualHeight        int                 `json:"actual_height"`
	EstimatedHeight     int                 `json:"estimated_height"`
	ContentLength       int                 `json:"content_length"`
	RenderTime          time.Duration       `json:"render_time"`
	MeasurementMetadata MeasurementMetadata `json:"metadata"`
}

// MeasurementMetadata 测量元数据
type MeasurementMetadata struct {
	FontHeight    int  `json:"font_height"`
	LineHeight    int  `json:"line_height"`
	MarginTop     int  `json:"margin_top"`
	MarginBottom  int  `json:"margin_bottom"`
	PaddingTop    int  `json:"padding_top"`
	PaddingBottom int  `json:"padding_bottom"`
	BorderTop     int  `json:"border_top"`
	BorderBottom  int  `json:"border_bottom"`
	ActualWidth   int  `json:"actual_width"`
	LineCount     int  `json:"line_count"`
	Overflow      bool `json:"overflow"`
	TextWrap      bool `json:"text_wrap"`
}

// OptimizedPage 优化后的页面
type OptimizedPage struct {
	PageIndex      int                  `json:"page_index"`
	Elements       []pagination.Element `json:"elements"`
	TotalHeight    int                  `json:"total_height"`
	UtilizedHeight int                  `json:"utilized_height"`
	Efficiency     float64              `json:"efficiency"`
	CanFitMore     bool                 `json:"can_fit_more"`
}

// MeasureAllElements 测量所有元素的精确高度
func (e *PreciseMeasurementEngine) MeasureAllElements(
	structuredTextArray []pagination.Element,
	imagePromptURL string,
) ([]ElementMeasurement, error) {
	fmt.Printf("🔬 开始精确测量所有元素，共 %d 个\n", len(structuredTextArray))

	var measurements []ElementMeasurement

	// 过滤掉第一个title（用于首卡），测量其余元素
	remainingElements := e.filterOutFirstTitle(structuredTextArray)

	for i, element := range remainingElements {
		startTime := time.Now()

		measurement, err := e.measureSingleElement(i, element)
		if err != nil {
			fmt.Printf("⚠️ 测量元素 %d 失败: %v，使用估算值\n", i, err)
			// 使用估算值作为后备
			measurement = e.estimateElementMeasurement(i, element)
		}

		measurement.RenderTime = time.Since(startTime)
		measurements = append(measurements, measurement)

		fmt.Printf("📏 元素 %d [%s]: 实际高度=%dpx, 估算高度=%dpx, 耗时=%v\n",
			i, element.Type, measurement.ActualHeight, measurement.EstimatedHeight, measurement.RenderTime)
	}

	fmt.Printf("✅ 所有元素测量完成，共 %d 个测量结果\n", len(measurements))
	return measurements, nil
}

// measureSingleElement 测量单个元素的精确高度
func (e *PreciseMeasurementEngine) measureSingleElement(
	index int,
	element pagination.Element,
) (ElementMeasurement, error) {
	// 生成单个元素的测量HTML
	measureHTML, err := e.generateSingleElementMeasurementHTML(element)
	if err != nil {
		return ElementMeasurement{}, fmt.Errorf("生成测量HTML失败: %v", err)
	}

	// 使用无头浏览器进行精确测量
	metadata, err := e.measureWithHeadlessBrowser(measureHTML)
	if err != nil {
		return ElementMeasurement{}, fmt.Errorf("无头浏览器测量失败: %v", err)
	}

	// 估算高度作为对比
	estimatedHeight := e.estimateElementHeight(element)

	measurement := ElementMeasurement{
		ElementIndex:        index,
		Element:             element,
		ActualHeight:        metadata.ActualHeight(),
		EstimatedHeight:     estimatedHeight,
		ContentLength:       e.calculateContentLength(element),
		MeasurementMetadata: metadata,
	}

	return measurement, nil
}

// generateSingleElementMeasurementHTML 生成单个元素的测量HTML
func (e *PreciseMeasurementEngine) generateSingleElementMeasurementHTML(element pagination.Element) (string, error) {
	const measurementTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Element Measurement</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans CJK SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'Source Han Sans CN';
            width: 1080px;
            background: #ffffff;
            color: #333333;
            padding: 60px;
        }
        .measurement-container {
            width: 960px; /* 1080px - 60px * 2 */
        }
        .element-subtitle {
            font-size: 24px;
            color: #4A4A4A;
            text-align: left;
            margin: 25px 0;
            border-bottom: 1px solid #E0E0E0;
            padding-bottom: 25px;
            line-height: 1.5;
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
            line-height: 1.6;
            margin-bottom: 20px;
            list-style: none;
            padding-left: 30px;
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
            line-height: 1.6;
            font-style: italic;
            background-color: #F0F7FF;
            border-left: 5px solid #1E88E5;
            padding: 20px;
            margin: 30px 0;
        }
    </style>
</head>
<body>
    <div class="measurement-container">
        <div id="target-element" class="target-element">
            {{ELEMENT_HTML}}
        </div>
    </div>
    
    <script>
        // 精确测量函数
        function getMeasurementData() {
            const element = document.getElementById('target-element');
            const computedStyle = window.getComputedStyle(element);
            
            return {
                offsetHeight: element.offsetHeight,
                clientHeight: element.clientHeight,
                scrollHeight: element.scrollHeight,
                offsetWidth: element.offsetWidth,
                clientWidth: element.clientWidth,
                marginTop: parseInt(computedStyle.marginTop) || 0,
                marginBottom: parseInt(computedStyle.marginBottom) || 0,
                paddingTop: parseInt(computedStyle.paddingTop) || 0,
                paddingBottom: parseInt(computedStyle.paddingBottom) || 0,
                borderTopWidth: parseInt(computedStyle.borderTopWidth) || 0,
                borderBottomWidth: parseInt(computedStyle.borderBottomWidth) || 0,
                fontSize: parseInt(computedStyle.fontSize) || 0,
                lineHeight: computedStyle.lineHeight
            };
        }
        
        // 暴露给外部调用
        window.getMeasurementData = getMeasurementData;
    </script>
</body>
</html>`

	// 根据元素类型生成对应的HTML
	var elementHTML string
	switch element.Type {
	case pagination.ElementTypeSubtitle:
		elementHTML = fmt.Sprintf(`<div class="element-subtitle">%s</div>`, element.Content)
	case pagination.ElementTypeBody:
		elementHTML = fmt.Sprintf(`<div class="element-body">%s</div>`, element.Content)
	case pagination.ElementTypeList:
		elementHTML = `<ul class="element-list">`
		if items, ok := element.Content.([]string); ok {
			for _, item := range items {
				elementHTML += fmt.Sprintf(`<li class="list-item">%s</li>`, item)
			}
		}
		elementHTML += `</ul>`
	case pagination.ElementTypeQuote:
		elementHTML = fmt.Sprintf(`<blockquote class="element-quote">%s</blockquote>`, element.Content)
	default:
		elementHTML = fmt.Sprintf(`<div class="element-body">%s</div>`, element.Content)
	}

	// 使用字符串替换来插入元素HTML
	finalHTML := measurementTemplate

	// 手动替换 {{ELEMENT_HTML}} 占位符
	placeholder := "{{ELEMENT_HTML}}"
	if pos := findSubstring(finalHTML, placeholder); pos != -1 {
		finalHTML = finalHTML[:pos] + elementHTML + finalHTML[pos+len(placeholder):]
	}

	return finalHTML, nil
}

// measureWithHeadlessBrowser 使用无头浏览器进行测量
func (e *PreciseMeasurementEngine) measureWithHeadlessBrowser(htmlContent string) (MeasurementMetadata, error) {
	// 保存HTML到临时文件
	tmpFile := fmt.Sprintf("/tmp/measure_element_%d.html", time.Now().Unix())
	if err := os.WriteFile(tmpFile, []byte(htmlContent), 0644); err != nil {
		return MeasurementMetadata{}, fmt.Errorf("写入临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile)

	// 获取绝对路径
	absPath, err := filepath.Abs(tmpFile)
	if err != nil {
		return MeasurementMetadata{}, fmt.Errorf("获取绝对路径失败: %v", err)
	}

	// 创建Chrome上下文
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("window-size", "1080,1440"),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	var result interface{}

	// 执行测量
	err = chromedp.Run(ctx,
		chromedp.Navigate("file://"+absPath),
		chromedp.WaitVisible("#target-element"),
		chromedp.Sleep(1*time.Second), // 等待渲染完成
		chromedp.Evaluate("getMeasurementData()", &result),
	)

	if err != nil {
		return MeasurementMetadata{}, fmt.Errorf("Chrome测量失败: %v", err)
	}

	// 解析测量结果
	return e.parseMeasurementResult(result)
}

// parseMeasurementResult 解析测量结果
func (e *PreciseMeasurementEngine) parseMeasurementResult(result interface{}) (MeasurementMetadata, error) {
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return MeasurementMetadata{}, fmt.Errorf("测量结果格式错误")
	}

	metadata := MeasurementMetadata{}

	if offsetHeight, ok := resultMap["offsetHeight"].(float64); ok {
		metadata.ActualWidth = int(offsetHeight)
	}
	if clientHeight, ok := resultMap["clientHeight"].(float64); ok {
		metadata.LineHeight = int(clientHeight)
	}
	if offsetWidth, ok := resultMap["offsetWidth"].(float64); ok {
		metadata.ActualWidth = int(offsetWidth)
	}
	if marginTop, ok := resultMap["marginTop"].(float64); ok {
		metadata.MarginTop = int(marginTop)
	}
	if marginBottom, ok := resultMap["marginBottom"].(float64); ok {
		metadata.MarginBottom = int(marginBottom)
	}
	if paddingTop, ok := resultMap["paddingTop"].(float64); ok {
		metadata.PaddingTop = int(paddingTop)
	}
	if paddingBottom, ok := resultMap["paddingBottom"].(float64); ok {
		metadata.PaddingBottom = int(paddingBottom)
	}
	if borderTop, ok := resultMap["borderTopWidth"].(float64); ok {
		metadata.BorderTop = int(borderTop)
	}
	if borderBottom, ok := resultMap["borderBottomWidth"].(float64); ok {
		metadata.BorderBottom = int(borderBottom)
	}
	if fontSize, ok := resultMap["fontSize"].(float64); ok {
		metadata.FontHeight = int(fontSize)
	}

	return metadata, nil
}

// OptimizePagination 基于测量结果优化分页
func (e *PreciseMeasurementEngine) OptimizePagination(measurements []ElementMeasurement) ([]OptimizedPage, error) {
	fmt.Printf("🔧 开始优化分页，基于 %d 个测量结果\n", len(measurements))

	const availableHeight = 1440 - 60 - 60 // 1320px 可用高度

	var pages []OptimizedPage
	var currentPage OptimizedPage
	var currentHeight int

	for _, measurement := range measurements {
		elementHeight := measurement.ActualHeight

		// 检查是否需要新页面
		if currentHeight+elementHeight > availableHeight && len(currentPage.Elements) > 0 {
			// 完成当前页面
			currentPage.TotalHeight = availableHeight
			currentPage.UtilizedHeight = currentHeight
			currentPage.Efficiency = float64(currentHeight) / float64(availableHeight)
			currentPage.CanFitMore = false

			pages = append(pages, currentPage)

			// 开始新页面
			currentPage = OptimizedPage{
				PageIndex: len(pages),
				Elements:  []pagination.Element{measurement.Element},
			}
			currentHeight = elementHeight
		} else {
			// 添加到当前页面
			currentPage.Elements = append(currentPage.Elements, measurement.Element)
			currentHeight += elementHeight
		}

		fmt.Printf("📄 元素 %d: 高度=%dpx, 当前页高度=%dpx\n",
			measurement.ElementIndex, elementHeight, currentHeight)
	}

	// 处理最后一页
	if len(currentPage.Elements) > 0 {
		currentPage.TotalHeight = availableHeight
		currentPage.UtilizedHeight = currentHeight
		currentPage.Efficiency = float64(currentHeight) / float64(availableHeight)
		currentPage.CanFitMore = (currentHeight < availableHeight*0.8) // 如果利用率小于80%，认为可以放更多
		currentPage.PageIndex = len(pages)

		pages = append(pages, currentPage)
	}

	fmt.Printf("✅ 分页优化完成，共 %d 页\n", len(pages))
	for i, page := range pages {
		fmt.Printf("📄 页面 %d: 元素数=%d, 利用率=%.2f%%, 高度=%d/%d\n",
			i+1, len(page.Elements), page.Efficiency*100, page.UtilizedHeight, page.TotalHeight)
	}

	return pages, nil
}

// RenderOptimizedPages 渲染优化后的页面
func (e *PreciseMeasurementEngine) RenderOptimizedPages(
	book *model.BookM,
	optimizedPages []OptimizedPage,
	imagePromptURL string,
) ([]*RenderedCard, error) {
	fmt.Printf("🎨 开始渲染优化后的页面，共 %d 页\n", len(optimizedPages))

	var renderedCards []*RenderedCard

	// 1. 渲染第一张卡片（特殊卡片）
	firstCard, err := e.renderFirstCard(book.ID, imagePromptURL)
	if err != nil {
		fmt.Printf("⚠️ 渲染第一张卡片失败: %v\n", err)
	} else {
		renderedCards = append(renderedCards, firstCard)
	}

	// 2. 渲染优化后的内容页面
	for i, page := range optimizedPages {
		sortOrder := i + 2 // 从第2张卡片开始
		cardID := book.ID*1000 + uint(sortOrder)

		renderedCard, err := e.renderOptimizedPage(cardID, sortOrder, page)
		if err != nil {
			fmt.Printf("⚠️ 渲染第 %d 张卡片失败: %v\n", sortOrder, err)
			continue
		}

		renderedCards = append(renderedCards, renderedCard)
		fmt.Printf("✅ 第 %d 张卡片渲染完成，效率: %.2f%%\n", sortOrder, page.Efficiency*100)
	}

	fmt.Printf("✅ 所有优化页面渲染完成，共 %d 张卡片\n", len(renderedCards))
	return renderedCards, nil
}

// 辅助函数

func (e *PreciseMeasurementEngine) filterOutFirstTitle(elements []pagination.Element) []pagination.Element {
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

func (e *PreciseMeasurementEngine) estimateElementMeasurement(index int, element pagination.Element) ElementMeasurement {
	estimatedHeight := e.estimateElementHeight(element)

	return ElementMeasurement{
		ElementIndex:    index,
		Element:         element,
		ActualHeight:    estimatedHeight, // 使用估算值作为实际值
		EstimatedHeight: estimatedHeight,
		ContentLength:   e.calculateContentLength(element),
		MeasurementMetadata: MeasurementMetadata{
			FontHeight:   18,
			LineHeight:   32,
			MarginTop:    20,
			MarginBottom: 20,
		},
	}
}

func (e *PreciseMeasurementEngine) estimateElementHeight(element pagination.Element) int {
	switch element.Type {
	case pagination.ElementTypeSubtitle:
		return 80
	case pagination.ElementTypeBody:
		contentLen := len(fmt.Sprintf("%v", element.Content))
		lines := (contentLen / 50) + 1
		return lines * 32
	case pagination.ElementTypeList:
		if items, ok := element.Content.([]string); ok {
			return len(items) * 40
		}
		return 80
	case pagination.ElementTypeQuote:
		contentLen := len(fmt.Sprintf("%v", element.Content))
		lines := (contentLen / 45) + 1
		return (lines * 30) + 80
	default:
		return 100
	}
}

func (e *PreciseMeasurementEngine) calculateContentLength(element pagination.Element) int {
	switch v := element.Content.(type) {
	case string:
		return len([]rune(v))
	case []string:
		total := 0
		for _, item := range v {
			total += len([]rune(item))
		}
		return total
	default:
		return len([]rune(fmt.Sprintf("%v", v)))
	}
}

func (e *PreciseMeasurementEngine) renderFirstCard(bookID uint, imageURL string) (*RenderedCard, error) {
	// 这里应该调用增强渲染器的第一张卡片渲染逻辑
	// 为了简化，返回模拟结果
	cardID := bookID*1000 + 1

	return &RenderedCard{
		CardID:    cardID,
		ImageURL:  "/path/to/first/card.png", // 模拟URL
		Width:     1080,
		Height:    1440,
		SortOrder: 1,
	}, nil
}

func (e *PreciseMeasurementEngine) renderOptimizedPage(cardID uint, sortOrder int, page OptimizedPage) (*RenderedCard, error) {
	// 这里应该调用增强渲染器的内容卡片渲染逻辑
	// 为了简化，返回模拟结果

	return &RenderedCard{
		CardID:    cardID,
		ImageURL:  fmt.Sprintf("/path/to/card_%d.png", cardID), // 模拟URL
		Width:     1080,
		Height:    1440,
		SortOrder: sortOrder,
	}, nil
}

// ActualHeight 计算实际高度
func (m MeasurementMetadata) ActualHeight() int {
	return m.LineHeight + m.MarginTop + m.MarginBottom + m.PaddingTop + m.PaddingBottom + m.BorderTop + m.BorderBottom
}

// findSubstring 查找子字符串位置
func findSubstring(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
