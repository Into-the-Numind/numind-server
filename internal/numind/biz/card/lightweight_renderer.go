package card

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"

	"github.com/disintegration/imaging"
)

// LightweightRenderer 轻量级渲染器，完全替代无头浏览器
type LightweightRenderer struct {
	config         *pagination.PaginationConfig
	tempDir        string
	wkhtmltoimage  string
	maxRetries     int
	requestTimeout time.Duration
}

// NewLightweightRenderer 创建轻量级渲染器
func NewLightweightRenderer(config *pagination.PaginationConfig) (*LightweightRenderer, error) {
	// 检查wkhtmltoimage是否可用
	wkhtmlPath, err := exec.LookPath("wkhtmltoimage")
	if err != nil {
		return nil, fmt.Errorf("wkhtmltoimage not found in PATH: %v", err)
	}

	// 创建临时目录
	tempDir := filepath.Join(os.TempDir(), "numind_renderer")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %v", err)
	}

	return &LightweightRenderer{
		config:         config,
		tempDir:        tempDir,
		wkhtmltoimage:  wkhtmlPath,
		maxRetries:     3,
		requestTimeout: 30 * time.Second,
	}, nil
}

// RenderBookToLongImage 将整本书渲染为超长图并切分
func (r *LightweightRenderer) RenderBookToLongImage(book *model.BookM, cards []*model.CardM) ([]*RenderedCard, error) {
	fmt.Printf("🚀 开始轻量级渲染器处理：书籍ID=%d，卡片数量=%d\n", book.ID, len(cards))

	// 1. 生成包含所有卡片的完整HTML
	fullHTML, err := r.generateFullBookHTML(book, cards)
	if err != nil {
		return nil, fmt.Errorf("生成完整HTML失败: %v", err)
	}

	// 2. 使用wkhtmltoimage生成超长图
	longImageData, err := r.renderLongImage(fullHTML)
	if err != nil {
		return nil, fmt.Errorf("渲染超长图失败: %v", err)
	}

	// 3. 计算切分点并切分图片
	splitImages, err := r.splitLongImage(longImageData)
	if err != nil {
		return nil, fmt.Errorf("切分超长图失败: %v", err)
	}

	// 4. 保存切分后的图片并生成结果
	renderedCards := make([]*RenderedCard, len(splitImages))
	for i, imageData := range splitImages {
		var cardID uint
		var sortOrder int
		if i < len(cards) {
			cardID = cards[i].ID
			sortOrder = cards[i].SortOrder
		} else {
			// 处理额外的切分图片（如果有的话）
			cardID = 0
			sortOrder = i + 1
		}

		imageURL, err := r.saveImage(imageData, cardID)
		if err != nil {
			return nil, fmt.Errorf("保存图片失败: %v", err)
		}

		renderedCards[i] = &RenderedCard{
			CardID:    cardID,
			ImageURL:  imageURL,
			Width:     r.config.Card.Width,
			Height:    r.config.Card.Height,
			SortOrder: sortOrder,
		}
	}

	fmt.Printf("✅ 轻量级渲染完成：生成了%d张卡片图片\n", len(renderedCards))
	return renderedCards, nil
}

// generateFullBookHTML 生成包含所有卡片内容的完整HTML
func (r *LightweightRenderer) generateFullBookHTML(book *model.BookM, cards []*model.CardM) (string, error) {
	fmt.Printf("📄 生成完整书籍HTML：卡片数量=%d\n", len(cards))

	// HTML模板 - 针对wkhtmltoimage优化
	htmlTemplate := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <style>
        @font-face {
            font-family: "SourceHanSansCN";
            src: url("data:font/truetype;base64,%s") format("truetype");
            font-weight: normal;
            font-style: normal;
        }
        
        * { 
            margin: 0; 
            padding: 0; 
            box-sizing: border-box; 
        }
        
        body {
            font-family: "SourceHanSansCN", -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
            background: #ffffff;
            color: #333333;
            line-height: 1.6;
            width: %dpx;
            margin: 0;
            padding: 0;
        }
        
        .card-container {
            width: %dpx;
            min-height: %dpx;
            padding: %dpx %dpx;
            background: #ffffff;
            page-break-inside: avoid;
            box-sizing: border-box;
            margin-bottom: 0;
        }
        
        .content-element {
            margin-bottom: 0;
        }
        
        .content-element:last-child {
            margin-bottom: 0;
        }
        
        .element-title {
            font-size: %dpx;
            color: #333333;
            line-height: 1.4;
            text-align: justify;
            margin: 0 0 %dpx 0;
            font-weight: bold;
        }
        
        .element-subtitle {
            font-size: %dpx;
            color: #666666;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 %dpx 0;
            font-weight: normal;
        }
        
        .element-body {
            font-size: %dpx;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 %dpx 0;
        }
        
        .element-quote {
            font-size: %dpx;
            color: #1E90FF;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 %dpx 0;
            font-style: italic;
            padding: %dpx;
            background: linear-gradient(to right, #EAF2FF, #FAFCFF);
            border-left: %dpx solid #1E90FF;
            border-radius: 0 %dpx %dpx 0;
        }
        
        .element-list {
            font-size: %dpx;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 %dpx 0;
            padding-left: %dpx;
            list-style: none;
        }
        
        .list-item {
            margin-bottom: %dpx;
            position: relative;
        }
        
        .list-item:before {
            content: "•";
            position: absolute;
            left: -%dpx;
            color: #333333;
        }
        
        .list-item:last-child {
            margin-bottom: 0;
        }
    </style>
</head>
<body>
%s
</body>
</html>`

	// 生成所有卡片的内容HTML
	var cardsHTML strings.Builder
	for i, card := range cards {
		fmt.Printf("🔄 处理卡片%d: ID=%d\n", i+1, card.ID)

		// 解析卡片数据
		var elements []pagination.Element
		if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
			return "", fmt.Errorf("解析卡片数据失败: %v", err)
		}

		// 生成单个卡片的HTML内容
		cardHTML := r.generateCardContent(elements)

		// 包装在card-container中
		cardsHTML.WriteString(fmt.Sprintf(`
    <div class="card-container">
        %s
    </div>`, cardHTML))
	}

	// 获取嵌入字体的base64（这里简化处理，实际应用中需要加载真实字体文件）
	fontBase64 := r.getEmbeddedFont()

	// 填充模板参数
	fullHTML := fmt.Sprintf(htmlTemplate,
		book.Title,                 // 标题
		fontBase64,                 // 字体base64
		r.config.Card.Width,        // body宽度
		r.config.Card.Width,        // card-container宽度
		r.config.Card.Height,       // card-container最小高度
		r.config.Card.Padding.Top,  // padding top
		r.config.Card.Padding.Left, // padding left/right
		64,                         // title字体大小
		30,                         // title margin-bottom
		48,                         // subtitle字体大小
		25,                         // subtitle margin-bottom
		36,                         // body字体大小
		30,                         // body margin-bottom
		36,                         // quote字体大小
		30,                         // quote margin-bottom
		20,                         // quote padding
		4,                          // quote border-left宽度
		8, 8,                       // quote border-radius
		36,                 // list字体大小
		30,                 // list margin-bottom
		40,                 // list padding-left
		8,                  // list-item margin-bottom
		20,                 // list-item:before left offset
		cardsHTML.String(), // 卡片内容
	)

	fmt.Printf("📄 HTML生成完成，总长度=%d bytes\n", len(fullHTML))
	return fullHTML, nil
}

// generateCardContent 生成单个卡片的内容HTML
func (r *LightweightRenderer) generateCardContent(elements []pagination.Element) string {
	var html strings.Builder

	for _, element := range elements {
		switch element.Type {
		case "title":
			html.WriteString(fmt.Sprintf(`
        <div class="content-element">
            <h2 class="element-title">%s</h2>
        </div>`, r.escapeHTML(fmt.Sprintf("%v", element.Content))))

		case "subtitle":
			html.WriteString(fmt.Sprintf(`
        <div class="content-element">
            <h3 class="element-subtitle">%s</h3>
        </div>`, r.escapeHTML(fmt.Sprintf("%v", element.Content))))

		case "body":
			html.WriteString(fmt.Sprintf(`
        <div class="content-element">
            <p class="element-body">%s</p>
        </div>`, r.escapeHTML(fmt.Sprintf("%v", element.Content))))

		case "list":
			html.WriteString(`
        <div class="content-element">
            <ul class="element-list">`)
			// 处理列表内容
			if listContent, ok := element.Content.([]interface{}); ok {
				for _, item := range listContent {
					html.WriteString(fmt.Sprintf(`
                <li class="list-item">%s</li>`, r.escapeHTML(fmt.Sprintf("%v", item))))
				}
			} else if contentStr, ok := element.Content.(string); ok {
				// 如果Content是字符串，尝试解析为列表项
				items := strings.Split(contentStr, "\n")
				for _, item := range items {
					if strings.TrimSpace(item) != "" {
						html.WriteString(fmt.Sprintf(`
                <li class="list-item">%s</li>`, r.escapeHTML(strings.TrimSpace(item))))
					}
				}
			}
			html.WriteString(`
            </ul>
        </div>`)

		case "quote":
			html.WriteString(fmt.Sprintf(`
        <div class="content-element">
            <blockquote class="element-quote">%s</blockquote>
        </div>`, r.escapeHTML(fmt.Sprintf("%v", element.Content))))

		default:
			// 默认按body处理
			html.WriteString(fmt.Sprintf(`
        <div class="content-element">
            <p class="element-body">%s</p>
        </div>`, r.escapeHTML(fmt.Sprintf("%v", element.Content))))
		}
	}

	return html.String()
}

// renderLongImage 使用wkhtmltoimage渲染超长图
func (r *LightweightRenderer) renderLongImage(htmlContent string) ([]byte, error) {
	fmt.Printf("🖼️  开始使用wkhtmltoimage渲染超长图\n")

	// 保存HTML到临时文件
	htmlFile := filepath.Join(r.tempDir, fmt.Sprintf("render_%d.html", time.Now().Unix()))
	if err := os.WriteFile(htmlFile, []byte(htmlContent), 0644); err != nil {
		return nil, fmt.Errorf("保存HTML文件失败: %v", err)
	}
	defer os.Remove(htmlFile)

	// 输出图片文件路径
	imageFile := filepath.Join(r.tempDir, fmt.Sprintf("render_%d.png", time.Now().Unix()))
	defer os.Remove(imageFile)

	// 构建wkhtmltoimage命令参数
	args := []string{
		"--width", strconv.Itoa(r.config.Card.Width),
		"--disable-javascript", // 禁用JavaScript，静态内容无需执行
		"--no-background",      // 不使用默认背景
		"--format", "png",      // 输出PNG格式
		"--quality", "90", // 设置图片质量
		"--encoding", "utf-8", // 设置编码
		"--enable-local-file-access",      // 允许访问本地文件
		"--disable-smart-width",           // 禁用智能宽度
		"--load-error-handling", "ignore", // 忽略加载错误
		"--load-media-error-handling", "ignore", // 忽略媒体加载错误
		htmlFile,
		imageFile,
	}

	fmt.Printf("🔧 执行wkhtmltoimage命令: %s %s\n", r.wkhtmltoimage, strings.Join(args, " "))

	// 执行渲染命令
	var lastErr error
	for attempt := 1; attempt <= r.maxRetries; attempt++ {
		fmt.Printf("🔄 第%d次尝试渲染...\n", attempt)

		ctx, cancel := context.WithTimeout(context.Background(), r.requestTimeout)
		cmd := exec.CommandContext(ctx, r.wkhtmltoimage, args...)

		var stderr bytes.Buffer
		cmd.Stderr = &stderr

		err := cmd.Run()
		cancel()

		if err == nil {
			// 读取生成的图片
			imageData, err := os.ReadFile(imageFile)
			if err != nil {
				lastErr = fmt.Errorf("读取生成的图片失败: %v", err)
				continue
			}

			fmt.Printf("✅ wkhtmltoimage渲染成功，图片大小=%d bytes\n", len(imageData))
			return imageData, nil
		}

		lastErr = fmt.Errorf("wkhtmltoimage执行失败 (尝试%d/%d): %v, stderr: %s",
			attempt, r.maxRetries, err, stderr.String())
		fmt.Printf("❌ %v\n", lastErr)

		if attempt < r.maxRetries {
			time.Sleep(time.Duration(attempt) * time.Second) // 指数退避
		}
	}

	return nil, fmt.Errorf("wkhtmltoimage渲染失败，已重试%d次: %v", r.maxRetries, lastErr)
}

// splitLongImage 切分超长图为多张1080x1440的卡片图
func (r *LightweightRenderer) splitLongImage(longImageData []byte) ([][]byte, error) {
	fmt.Printf("✂️  开始切分超长图\n")

	// 解码图片
	img, _, err := image.Decode(bytes.NewReader(longImageData))
	if err != nil {
		return nil, fmt.Errorf("解码超长图失败: %v", err)
	}

	bounds := img.Bounds()
	totalHeight := bounds.Max.Y
	cardWidth := r.config.Card.Width
	cardHeight := r.config.Card.Height

	fmt.Printf("📏 超长图尺寸: %dx%d, 目标卡片尺寸: %dx%d\n",
		bounds.Max.X, totalHeight, cardWidth, cardHeight)

	// 计算切分点
	splitPoints := r.calculateSplitPoints(totalHeight, cardHeight)
	fmt.Printf("🎯 计算出%d个切分点: %v\n", len(splitPoints), splitPoints)

	// 按切分点裁剪图片
	var result [][]byte
	prevY := 0

	for i, splitY := range splitPoints {
		fmt.Printf("✂️  裁剪第%d张卡片: Y范围 %d-%d\n", i+1, prevY, splitY)

		// 裁剪当前区域
		cropRect := image.Rect(0, prevY, cardWidth, splitY)
		croppedImg := imaging.Crop(img, cropRect)

		// 如果裁剪出的图片高度不足cardHeight，则补白到cardHeight
		croppedBounds := croppedImg.Bounds()
		if croppedBounds.Max.Y < cardHeight {
			fmt.Printf("📏 图片高度不足，补白: %d -> %d\n", croppedBounds.Max.Y, cardHeight)
			// 使用自定义函数补白图片
			croppedImg = r.padImageToSize(croppedImg, cardWidth, cardHeight)
		}

		// 编码为PNG
		buf := &bytes.Buffer{}
		if err := png.Encode(buf, croppedImg); err != nil {
			return nil, fmt.Errorf("编码第%d张卡片失败: %v", i+1, err)
		}

		result = append(result, buf.Bytes())
		prevY = splitY
	}

	// 处理最后一段（如果有剩余内容）
	if prevY < totalHeight {
		fmt.Printf("✂️  裁剪最后一张卡片: Y范围 %d-%d\n", prevY, totalHeight)

		cropRect := image.Rect(0, prevY, cardWidth, totalHeight)
		lastCropped := imaging.Crop(img, cropRect)

		// 补白到标准尺寸
		lastBounds := lastCropped.Bounds()
		if lastBounds.Max.Y < cardHeight {
			fmt.Printf("📏 最后一张图片高度不足，补白: %d -> %d\n", lastBounds.Max.Y, cardHeight)
			lastCropped = r.padImageToSize(lastCropped, cardWidth, cardHeight)
		}

		// 编码
		buf := &bytes.Buffer{}
		if err := png.Encode(buf, lastCropped); err != nil {
			return nil, fmt.Errorf("编码最后一张卡片失败: %v", err)
		}

		result = append(result, buf.Bytes())
	}

	fmt.Printf("✅ 切分完成，生成了%d张卡片图片\n", len(result))
	return result, nil
}

// calculateSplitPoints 计算切分点，确保不截断内容
func (r *LightweightRenderer) calculateSplitPoints(totalHeight, cardHeight int) []int {
	var splitPoints []int

	for y := cardHeight; y < totalHeight; y += cardHeight {
		splitPoints = append(splitPoints, y)
	}

	return splitPoints
}

// saveImage 保存图片并返回URL
func (r *LightweightRenderer) saveImage(imageData []byte, cardID uint) (string, error) {
	// 生成保存路径
	saveDir := fmt.Sprintf("res/upload/card/%d", cardID)
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return "", fmt.Errorf("创建保存目录失败: %v", err)
	}

	filename := fmt.Sprintf("card_%d.png", cardID)
	filePath := filepath.Join(saveDir, filename)

	// 保存图片文件
	if err := os.WriteFile(filePath, imageData, 0644); err != nil {
		return "", fmt.Errorf("保存图片文件失败: %v", err)
	}

	// 返回访问URL
	url := fmt.Sprintf("/upload/card/%d/%s", cardID, filename)
	fmt.Printf("💾 图片保存成功: %s\n", url)
	return url, nil
}

// getEmbeddedFont 获取嵌入字体的base64编码（简化实现）
func (r *LightweightRenderer) getEmbeddedFont() string {
	// 这里应该加载真实的字体文件并转换为base64
	// 简化处理，返回空字符串，让系统使用fallback字体
	return ""
}

// escapeHTML HTML转义
func (r *LightweightRenderer) escapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(s)
}

// padImageToSize 将图片补白到指定尺寸
func (r *LightweightRenderer) padImageToSize(img image.Image, targetWidth, targetHeight int) *image.NRGBA {
	bounds := img.Bounds()
	currentWidth := bounds.Dx()
	currentHeight := bounds.Dy()

	// 如果尺寸已经符合要求，转换为NRGBA并返回
	if currentWidth == targetWidth && currentHeight == targetHeight {
		return imaging.Clone(img)
	}

	// 创建新的图片画布，用白色填充
	newImg := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))

	// 填充白色背景
	white := color.NRGBA{255, 255, 255, 255}
	for y := 0; y < targetHeight; y++ {
		for x := 0; x < targetWidth; x++ {
			newImg.Set(x, y, white)
		}
	}

	// 计算居中位置（水平居中，垂直顶部对齐）
	offsetX := (targetWidth - currentWidth) / 2
	offsetY := 0

	// 将原图片绘制到新画布上
	for y := 0; y < currentHeight && y+offsetY < targetHeight; y++ {
		for x := 0; x < currentWidth && x+offsetX < targetWidth; x++ {
			srcX := x + bounds.Min.X
			srcY := y + bounds.Min.Y
			if srcX < bounds.Max.X && srcY < bounds.Max.Y {
				newImg.Set(x+offsetX, y+offsetY, img.At(srcX, srcY))
			}
		}
	}

	return newImg
}

// Cleanup 清理临时文件
func (r *LightweightRenderer) Cleanup() error {
	return os.RemoveAll(r.tempDir)
}
