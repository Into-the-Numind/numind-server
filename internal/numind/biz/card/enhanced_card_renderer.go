package card

import (
	"context"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"github.com/chromedp/chromedp"
)

// EnhancedCardRenderer 增强版卡片渲染器
// 实现第一张卡片特殊布局（阿里文生图+标题）和后续卡片的分页渲染
type EnhancedCardRenderer struct {
	config *pagination.PaginationConfig
}

// NewEnhancedCardRenderer 创建增强版卡片渲染器
func NewEnhancedCardRenderer(config *pagination.PaginationConfig) *EnhancedCardRenderer {
	return &EnhancedCardRenderer{
		config: config,
	}
}

// CardRenderData 卡片渲染数据
type CardRenderData struct {
	IsFirstCard  bool                         `json:"is_first_card"`
	ImageURL     string                       `json:"image_url"`     // 阿里文生图URL（仅第一张卡片）
	TitleContent string                       `json:"title_content"` // 标题内容（仅第一张卡片）
	Elements     []CardElementData            `json:"elements"`      // 卡片元素（非第一张卡片）
	Config       *pagination.PaginationConfig `json:"config"`
}

// CardElementData 卡片元素数据
type CardElementData struct {
	Type    string   `json:"type"`
	Content string   `json:"content"`
	Items   []string `json:"items,omitempty"` // 用于 list 类型
}

// RenderBookWithPagination 渲染整本书，实现分页逻辑
func (r *EnhancedCardRenderer) RenderBookWithPagination(book *model.BookM, structuredTextArray []pagination.Element, imagePromptURL string) ([]*RenderedCard, error) {
	fmt.Printf("🚀 开始增强版卡片渲染，书籍: %s\n", book.Title)

	// 1. 提取标题内容
	titleContent := r.extractTitleContent(structuredTextArray)

	// 2. 过滤掉已使用的标题，获取后续内容元素
	remainingElements := r.filterOutUsedTitle(structuredTextArray)

	// 3. 渲染第一张卡片（特殊布局）
	firstCard, err := r.renderFirstCard(book.ID, 1, imagePromptURL, titleContent)
	if err != nil {
		return nil, fmt.Errorf("渲染第一张卡片失败: %v", err)
	}

	var renderedCards []*RenderedCard
	renderedCards = append(renderedCards, firstCard)

	// 4. 测量和分页后续元素
	cardPages, err := r.measureAndPaginateElements(remainingElements)
	if err != nil {
		return nil, fmt.Errorf("测量分页失败: %v", err)
	}

	// 5. 渲染后续卡片
	for pageIndex, pageElements := range cardPages {
		sortOrder := pageIndex + 2               // 从第2张卡片开始
		cardID := book.ID*1000 + uint(sortOrder) // 生成唯一卡片ID

		renderedCard, err := r.renderContentCard(cardID, sortOrder, pageElements)
		if err != nil {
			fmt.Printf("⚠️ 渲染第 %d 张卡片失败: %v\n", sortOrder, err)
			continue
		}

		renderedCards = append(renderedCards, renderedCard)
		fmt.Printf("✅ 第 %d 张卡片渲染完成\n", sortOrder)
	}

	fmt.Printf("✅ 所有卡片渲染完成，共 %d 张\n", len(renderedCards))
	return renderedCards, nil
}

// extractTitleContent 提取标题内容
func (r *EnhancedCardRenderer) extractTitleContent(elements []pagination.Element) string {
	for _, element := range elements {
		if element.Type == pagination.ElementTypeTitle {
			if content, ok := element.Content.(string); ok {
				return content
			}
		}
	}
	return "默认标题" // 如果没有找到标题，使用默认值
}

// filterOutUsedTitle 过滤掉已使用的标题，返回剩余元素
func (r *EnhancedCardRenderer) filterOutUsedTitle(elements []pagination.Element) []pagination.Element {
	var remaining []pagination.Element
	titleUsed := false

	fmt.Printf("🔍 调试：开始过滤title，原始元素数量: %d\n", len(elements))

	for i, element := range elements {
		fmt.Printf("🔍 调试：检查元素 %d，类型: %s\n", i, element.Type)

		if element.Type == pagination.ElementTypeTitle && !titleUsed {
			titleUsed = true // 跳过第一个标题
			fmt.Printf("🔍 调试：跳过title元素 %d\n", i)
			continue
		}
		remaining = append(remaining, element)
		fmt.Printf("🔍 调试：保留元素 %d，当前剩余数量: %d\n", i, len(remaining))

		// 如果是list类型，打印list内容
		if element.Type == pagination.ElementTypeList {
			if items, ok := element.Content.([]string); ok {
				fmt.Printf("🔍 调试：保留的list元素包含 %d 项\n", len(items))
				for j, item := range items {
					fmt.Printf("🔍 调试：list项 %d: %s\n", j, item)
				}
			}
		}
	}

	fmt.Printf("🔍 调试：过滤完成，最终剩余元素数量: %d\n", len(remaining))
	return remaining
}

// renderFirstCard 渲染第一张卡片（特殊布局）
func (r *EnhancedCardRenderer) renderFirstCard(bookID uint, sortOrder int, imageURL, titleContent string) (*RenderedCard, error) {
	fmt.Printf("🎨 渲染第一张卡片，图片URL: %s, 标题: %s\n", imageURL, titleContent)

	// 准备第一张卡片的渲染数据
	data := CardRenderData{
		IsFirstCard:  true,
		ImageURL:     imageURL,
		TitleContent: titleContent,
		Config:       r.config,
	}

	// 生成HTML内容
	htmlContent, err := r.generateFirstCardHTML(data)
	if err != nil {
		return nil, fmt.Errorf("生成第一张卡片HTML失败: %v", err)
	}

	// 使用无头浏览器渲染
	imageData, err := r.renderWithHeadlessBrowser(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("无头浏览器渲染失败: %v", err)
	}

	// 保存图片
	cardID := bookID*1000 + uint(sortOrder)
	imageURL, err = r.saveImageData(imageData, cardID)
	if err != nil {
		return nil, fmt.Errorf("保存图片失败: %v", err)
	}

	return &RenderedCard{
		CardID:    cardID,
		ImageURL:  imageURL,
		Width:     1080,
		Height:    1440,
		SortOrder: sortOrder,
	}, nil
}

// measureAndPaginateElements 测量元素高度并进行分页
func (r *EnhancedCardRenderer) measureAndPaginateElements(elements []pagination.Element) ([][]pagination.Element, error) {
	fmt.Printf("📏 开始测量和分页，共 %d 个元素\n", len(elements))

	if len(elements) == 0 {
		return [][]pagination.Element{}, nil
	}

	// 为每个元素生成HTML并测量高度
	elementHeights, err := r.measureElementHeights(elements)
	if err != nil {
		return nil, fmt.Errorf("测量元素高度失败: %v", err)
	}

	// 根据测量结果进行分页
	const availableHeight = 1440 - 60 - 60 // 1320px 可用高度（减去上下边距）

	var pages [][]pagination.Element
	var currentPage []pagination.Element
	var currentHeight int

	for i, element := range elements {
		elementHeight := elementHeights[i]

		fmt.Printf("🔍 调试：处理元素 %d，类型: %s，高度: %dpx\n", i+1, element.Type, elementHeight)

		// 如果是列表类型，详细检查内容
		if element.Type == pagination.ElementTypeList {
			if items, ok := element.Content.([]string); ok {
				fmt.Printf("🔍 调试：列表元素包含 %d 项:\n", len(items))
				for j, item := range items {
					fmt.Printf("🔍 调试：  项 %d: %s\n", j, item[:min(len(item), 50)]+"...")
				}
			}
		}

		// 检查是否需要新页面
		if currentHeight+elementHeight > availableHeight && len(currentPage) > 0 {
			// 当前页面已满，创建新页面
			fmt.Printf("🔍 调试：页面已满，创建新页面。当前页元素数: %d，当前高度: %d，新元素高度: %d\n",
				len(currentPage), currentHeight, elementHeight)
			pages = append(pages, currentPage)
			currentPage = []pagination.Element{element}
			currentHeight = elementHeight
			fmt.Printf("📄 创建新页面 %d，重置为元素 %d\n", len(pages), i+1)
		} else {
			// 添加到当前页面
			currentPage = append(currentPage, element)
			currentHeight += elementHeight
			fmt.Printf("🔍 调试：添加元素 %d 到当前页，页面元素数: %d，新高度: %d\n",
				i+1, len(currentPage), currentHeight)
		}

		fmt.Printf("📏 元素 %d [%s]: 高度=%dpx, 当前页高度=%dpx\n",
			i+1, element.Type, elementHeight, currentHeight)
	}

	// 添加最后一页
	fmt.Printf("🔍 调试：处理最后一页，剩余元素数: %d\n", len(currentPage))
	if len(currentPage) > 0 {
		fmt.Printf("🔍 调试：最后一页包含的元素:\n")
		for j, elem := range currentPage {
			fmt.Printf("🔍 调试：  最后页元素 %d: 类型=%s\n", j, elem.Type)
			if elem.Type == pagination.ElementTypeList {
				if items, ok := elem.Content.([]string); ok {
					fmt.Printf("🔍 调试：    最后页列表包含 %d 项\n", len(items))
					for k, item := range items {
						fmt.Printf("🔍 调试：      最后页项 %d: %s\n", k, item[:min(len(item), 30)]+"...")
					}
				}
			}
		}
		pages = append(pages, currentPage)
		fmt.Printf("🔍 调试：最后一页已添加，总页数: %d\n", len(pages))
	}

	fmt.Printf("✅ 分页完成，共 %d 页\n", len(pages))
	return pages, nil
}

// measureElementHeights 测量每个元素的实际高度
func (r *EnhancedCardRenderer) measureElementHeights(elements []pagination.Element) ([]int, error) {
	fmt.Printf("📐 开始测量元素高度，共 %d 个元素\n", len(elements))

	// 生成测量用的HTML
	measureHTML, err := r.generateMeasurementHTML(elements)
	if err != nil {
		return nil, fmt.Errorf("生成测量HTML失败: %v", err)
	}

	// 使用无头浏览器测量
	heights, err := r.measureHeightsWithBrowser(measureHTML, len(elements))
	if err != nil {
		return nil, fmt.Errorf("浏览器测量失败: %v", err)
	}

	fmt.Printf("✅ 高度测量完成，结果: %v\n", heights)
	return heights, nil
}

// renderContentCard 渲染内容卡片（非第一张）
func (r *EnhancedCardRenderer) renderContentCard(cardID uint, sortOrder int, elements []pagination.Element) (*RenderedCard, error) {
	// 转换元素格式
	var elementData []CardElementData
	for _, element := range elements {
		data := CardElementData{
			Type:    string(element.Type),
			Content: fmt.Sprintf("%v", element.Content),
		}

		// 处理列表类型
		if element.Type == pagination.ElementTypeList {
			if items, ok := element.Content.([]string); ok {
				data.Items = items
			} else if items, ok := element.Content.([]interface{}); ok {
				for _, item := range items {
					data.Items = append(data.Items, fmt.Sprintf("%v", item))
				}
			}
		}

		elementData = append(elementData, data)
	}

	// 准备渲染数据
	data := CardRenderData{
		IsFirstCard: false,
		Elements:    elementData,
		Config:      r.config,
	}

	// 生成HTML内容
	htmlContent, err := r.generateContentCardHTML(data)
	if err != nil {
		return nil, fmt.Errorf("生成内容卡片HTML失败: %v", err)
	}

	// 使用无头浏览器渲染
	imageData, err := r.renderWithHeadlessBrowser(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("无头浏览器渲染失败: %v", err)
	}

	// 保存图片
	imageURL, err := r.saveImageData(imageData, cardID)
	if err != nil {
		return nil, fmt.Errorf("保存图片失败: %v", err)
	}

	return &RenderedCard{
		CardID:    cardID,
		ImageURL:  imageURL,
		Width:     1080,
		Height:    1440,
		SortOrder: sortOrder,
	}, nil
}

// generateFirstCardHTML 生成第一张卡片的HTML
func (r *EnhancedCardRenderer) generateFirstCardHTML(data CardRenderData) (string, error) {
	const firstCardTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>First Card</title>
    <style>
        * { 
            margin: 0; 
            padding: 0; 
            box-sizing: border-box; 
        }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans CJK SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'Source Han Sans CN';
            width: 1080px;
            height: 1440px;
            margin: 0;
            padding: 0;
            overflow: hidden;
        }
        
        .first-card-container {
            width: 1080px;
            height: 1440px;
            display: flex;
            flex-direction: column;
        }
        
        .image-section {
            width: 1080px;
            height: 864px; /* 60% of 1440px */
            background-image: url('{{.ImageURL}}');
            background-size: cover;
            background-position: center;
            background-repeat: no-repeat;
        }
        
        .title-section {
            width: 1080px;
            height: 576px; /* 40% of 1440px */
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
            word-wrap: break-word;
        }
    </style>
</head>
<body>
    <div class="first-card-container">
        <div class="image-section"></div>
        <div class="title-section">
            <div class="title-text">{{.TitleContent}}</div>
        </div>
    </div>
</body>
</html>`

	tmpl, err := template.New("firstCard").Parse(firstCardTemplate)
	if err != nil {
		return "", fmt.Errorf("解析第一张卡片模板失败: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("执行第一张卡片模板失败: %v", err)
	}

	return buf.String(), nil
}

// generateContentCardHTML 生成内容卡片的HTML
func (r *EnhancedCardRenderer) generateContentCardHTML(data CardRenderData) (string, error) {
	const contentCardTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Content Card</title>
    <style>
        * { 
            margin: 0; 
            padding: 0; 
            box-sizing: border-box; 
        }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans CJK SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'Source Han Sans CN';
            width: 1080px;
            height: 1440px;
            margin: 0;
            padding: 60px;
            background: #ffffff;
            color: #333333;
            overflow: hidden;
        }
        
        .content-container {
            width: 960px; /* 1080px - 60px * 2 */
            height: 1320px; /* 1440px - 60px * 2 */
        }
        
        .content-element {
            margin-bottom: 0;
        }
        
        /* subtitle样式 */
        .element-subtitle {
            font-size: 24px;
            color: #4A4A4A;
            text-align: left;
            margin: 25px 0;
            border-bottom: 1px solid #E0E0E0;
            padding-bottom: 25px;
            line-height: 1.5;
        }
        
        /* body样式 */
        .element-body {
            font-size: 18px;
            color: #333333;
            line-height: 1.8;
            text-align: justify;
            margin-bottom: 20px;
        }
        
        /* list样式 */
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
        
        /* quote样式 */
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
        
        /* 第一个元素特殊处理 */
        .content-element:first-child {
            margin-top: 0;
        }
        
        /* 最后一个元素特殊处理 */
        .content-element:last-child {
            margin-bottom: 0;
        }
    </style>
</head>
<body>
    <div class="content-container">
        {{range .Elements}}
            {{if eq .Type "subtitle"}}
                <div class="content-element element-subtitle">{{.Content}}</div>
            {{else if eq .Type "body"}}
                <div class="content-element element-body">{{.Content}}</div>
            {{else if eq .Type "list"}}
                <ul class="content-element element-list">
                    {{range .Items}}
                        <li class="list-item">{{.}}</li>
                    {{end}}
                </ul>
            {{else if eq .Type "quote"}}
                <blockquote class="content-element element-quote">{{.Content}}</blockquote>
            {{else}}
                <div class="content-element element-body">{{.Content}}</div>
            {{end}}
        {{end}}
    </div>
</body>
</html>`

	tmpl, err := template.New("contentCard").Parse(contentCardTemplate)
	if err != nil {
		return "", fmt.Errorf("解析内容卡片模板失败: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("执行内容卡片模板失败: %v", err)
	}

	return buf.String(), nil
}

// generateMeasurementHTML 生成用于测量的HTML
func (r *EnhancedCardRenderer) generateMeasurementHTML(elements []pagination.Element) (string, error) {
	// 转换元素格式
	var elementData []CardElementData
	fmt.Printf("🔍 调试：开始生成测量HTML，元素数量: %d\n", len(elements))

	for i, element := range elements {
		fmt.Printf("🔍 调试：处理测量元素 %d，类型: %s\n", i, element.Type)
		data := CardElementData{
			Type:    string(element.Type),
			Content: fmt.Sprintf("%v", element.Content),
		}

		// 处理列表类型
		if element.Type == pagination.ElementTypeList {
			fmt.Printf("🔍 调试：处理列表类型元素 %d，内容类型: %T\n", i, element.Content)
			if items, ok := element.Content.([]string); ok {
				data.Items = items
				fmt.Printf("🔍 调试：列表转换成功([]string)，项目数: %d\n", len(items))
				for j, item := range items {
					fmt.Printf("🔍 调试：列表项 %d: %s\n", j, item)
				}
			} else if items, ok := element.Content.([]interface{}); ok {
				fmt.Printf("🔍 调试：列表内容为[]interface{}，原始长度: %d\n", len(items))
				for j, item := range items {
					converted := fmt.Sprintf("%v", item)
					data.Items = append(data.Items, converted)
					fmt.Printf("🔍 调试：列表项 %d 转换: %v -> %s\n", j, item, converted)
				}
				fmt.Printf("🔍 调试：列表转换完成，最终项目数: %d\n", len(data.Items))
			} else {
				fmt.Printf("🔍 调试：列表内容类型不匹配: %T，内容: %v\n", element.Content, element.Content)
			}
		}

		elementData = append(elementData, data)
		fmt.Printf("🔍 调试：元素 %d 处理完成，elementData长度: %d\n", i, len(elementData))
	}

	data := CardRenderData{
		IsFirstCard: false,
		Elements:    elementData,
		Config:      r.config,
	}

	return r.generateContentCardHTML(data)
}

// measureHeightsWithBrowser 使用浏览器测量元素高度
func (r *EnhancedCardRenderer) measureHeightsWithBrowser(htmlContent string, elementCount int) ([]int, error) {
	// 保存HTML到临时文件
	tmpFile := fmt.Sprintf("/tmp/measure_%d.html", time.Now().Unix())
	if err := os.WriteFile(tmpFile, []byte(htmlContent), 0644); err != nil {
		return nil, fmt.Errorf("写入临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile)

	// 获取绝对路径
	absPath, err := filepath.Abs(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("获取绝对路径失败: %v", err)
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

	// 测量脚本
	measureScript := `
		(function() {
			const elements = document.querySelectorAll('.content-element');
			const heights = [];
			for (let i = 0; i < elements.length; i++) {
				heights.push(elements[i].offsetHeight);
			}
			return heights;
		})();
	`

	var result interface{}

	// 执行测量
	err = chromedp.Run(ctx,
		chromedp.Navigate("file://"+absPath),
		chromedp.WaitVisible("body"),
		chromedp.Sleep(1*time.Second), // 等待渲染完成
		chromedp.Evaluate(measureScript, &result),
	)

	if err != nil {
		return nil, fmt.Errorf("Chrome执行失败: %v", err)
	}

	// 转换结果
	var heights []int
	if resultArray, ok := result.([]interface{}); ok {
		for _, height := range resultArray {
			if h, ok := height.(float64); ok {
				heights = append(heights, int(h))
			}
		}
	}

	if len(heights) != elementCount {
		return nil, fmt.Errorf("测量结果数量不匹配，期望 %d，实际 %d", elementCount, len(heights))
	}

	return heights, nil
}

// renderWithHeadlessBrowser 使用无头浏览器渲染HTML为图片
func (r *EnhancedCardRenderer) renderWithHeadlessBrowser(htmlContent string) ([]byte, error) {
	// 保存HTML到临时文件
	tmpFile := fmt.Sprintf("/tmp/render_%d.html", time.Now().Unix())
	if err := os.WriteFile(tmpFile, []byte(htmlContent), 0644); err != nil {
		return nil, fmt.Errorf("写入临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile)

	// 获取绝对路径
	absPath, err := filepath.Abs(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("获取绝对路径失败: %v", err)
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

	var imageData []byte

	// 渲染截图
	err = chromedp.Run(ctx,
		chromedp.Navigate("file://"+absPath),
		chromedp.WaitVisible("body"),
		chromedp.Sleep(2*time.Second), // 等待渲染完成
		chromedp.CaptureScreenshot(&imageData),
	)

	if err != nil {
		return nil, fmt.Errorf("Chrome渲染失败: %v", err)
	}

	return imageData, nil
}

// saveImageData 保存图片数据
func (r *EnhancedCardRenderer) saveImageData(imageData []byte, cardID uint) (string, error) {
	// 使用工具函数获取正确的图片保存路径
	cardDir := util.GetCardImagePath(cardID)
	fmt.Printf("🔍 增强渲染器：卡片保存目录=%s\n", cardDir)

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
	imageURL := util.GetCardImageURL(cardID, filename)
	fmt.Printf("🔍 增强渲染器：返回的图片URL=%s\n", imageURL)
	return imageURL, nil
}

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
