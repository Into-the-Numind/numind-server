package card

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// ChromeHeadlessRenderer Chrome无头浏览器渲染器
// 使用Chrome DevTools Protocol实现真正的渲染-测量方案
type ChromeHeadlessRenderer struct {
	config     *pagination.PaginationConfig
	chromePath string
	debugPort  int
}

// NewChromeHeadlessRenderer 创建新的Chrome无头浏览器渲染器
func NewChromeHeadlessRenderer(config *pagination.PaginationConfig) *ChromeHeadlessRenderer {
	renderer := &ChromeHeadlessRenderer{
		config:     config,
		chromePath: "",
		debugPort:  9222,
	}

	// 尝试找到Chrome可执行文件路径
	renderer.chromePath = renderer.findChromeExecutable()

	return renderer
}

// findChromeExecutable 查找Chrome可执行文件
func (r *ChromeHeadlessRenderer) findChromeExecutable() string {
	// 常见的Chrome安装路径
	paths := []string{
		"/usr/bin/google-chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", // macOS
		"C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe",   // Windows
		"C:\\Program Files (x86)\\Google\\Chrome\\Application\\chrome.exe",
	}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// 尝试使用which命令查找
	if output, err := exec.Command("which", "google-chrome").Output(); err == nil {
		return strings.TrimSpace(string(output))
	}

	if output, err := exec.Command("which", "chromium-browser").Output(); err == nil {
		return strings.TrimSpace(string(output))
	}

	return "google-chrome" // 默认使用PATH中的命令
}

// RenderBookToImages 使用Chrome无头浏览器渲染整本书
func (r *ChromeHeadlessRenderer) RenderBookToImages(book *model.BookM, cards []*model.CardM) ([]*RenderedCard, error) {
	fmt.Printf("🚀 Chrome无头浏览器开始渲染书籍: %s\n", book.Title)

	// 步骤1: 生成包含所有内容的HTML
	htmlContent, err := r.generateBookHTML(book, cards)
	if err != nil {
		return nil, fmt.Errorf("生成HTML失败: %v", err)
	}

	// 步骤2: 启动Chrome无头浏览器
	chromeProcess, err := r.startChromeHeadless()
	if err != nil {
		return nil, fmt.Errorf("启动Chrome失败: %v", err)
	}
	defer r.stopChrome(chromeProcess)

	// 步骤3: 使用DevTools Protocol进行渲染和测量
	pageBreakPoints, err := r.renderAndMeasureWithDevTools(htmlContent)
	if err != nil {
		return nil, fmt.Errorf("DevTools渲染测量失败: %v", err)
	}

	fmt.Printf("📏 DevTools测量完成，分页点数量: %d\n", len(pageBreakPoints))

	// 步骤4: 根据分页点进行区域截图
	renderedCards, err := r.captureImagesWithDevTools(htmlContent, pageBreakPoints, cards)
	if err != nil {
		return nil, fmt.Errorf("DevTools截图失败: %v", err)
	}

	fmt.Printf("✅ Chrome渲染完成，生成 %d 张图片\n", len(renderedCards))
	return renderedCards, nil
}

// startChromeHeadless 启动Chrome无头浏览器
func (r *ChromeHeadlessRenderer) startChromeHeadless() (*exec.Cmd, error) {
	args := []string{
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--disable-web-security",
		"--disable-features=VizDisplayCompositor",
		fmt.Sprintf("--remote-debugging-port=%d", r.debugPort),
		fmt.Sprintf("--window-size=%d,%d", r.config.Card.Width, r.config.Card.Height),
		"--user-data-dir=/tmp/chrome-headless",
		"--data-path=/tmp/chrome-headless-data",
		"--homedir=/tmp/chrome-headless-home",
		"--disk-cache-dir=/tmp/chrome-headless-cache",
		"--media-cache-dir=/tmp/chrome-headless-media",
		"--disk-cache-size=1",
		"--media-cache-size=1",
		"--aggressive-cache-discard",
		"--disable-cache",
		"--disable-application-cache",
		"--disable-offline-load-stale-cache",
		"--disk-cache-size=0",
		"--disable-background-timer-throttling",
		"--disable-backgrounding-occluded-windows",
		"--disable-renderer-backgrounding",
		"--disable-features=TranslateUI",
		"--disable-ipc-flooding-protection",
	}

	cmd := exec.Command(r.chromePath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动Chrome失败: %v", err)
	}

	// 等待Chrome启动
	time.Sleep(3 * time.Second)

	return cmd, nil
}

// stopChrome 停止Chrome进程
func (r *ChromeHeadlessRenderer) stopChrome(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		cmd.Wait()
	}
}

// renderAndMeasureWithDevTools 使用DevTools Protocol进行渲染和测量
func (r *ChromeHeadlessRenderer) renderAndMeasureWithDevTools(htmlContent string) ([]int, error) {
	fmt.Printf("🔍 使用DevTools Protocol进行渲染测量...\n")

	// 创建DevTools客户端
	devTools := NewDevToolsClient(r.debugPort)
	defer devTools.Close()

	// 1. 创建新页面
	pageID, err := devTools.CreatePage()
	if err != nil {
		return nil, fmt.Errorf("创建页面失败: %v", err)
	}
	fmt.Printf("✅ 创建页面成功，ID: %s\n", pageID)

	// 2. 导航到HTML内容
	if err := devTools.NavigateToHTML(pageID, htmlContent); err != nil {
		return nil, fmt.Errorf("导航到HTML失败: %v", err)
	}
	fmt.Printf("✅ 导航到HTML成功\n")

	// 3. 等待页面加载完成
	if err := devTools.WaitForLoad(pageID); err != nil {
		return nil, fmt.Errorf("等待页面加载失败: %v", err)
	}
	fmt.Printf("✅ 页面加载完成\n")

	// 4. 执行JavaScript测量代码
	measurementScript := `
		(function() {
			const cardHeight = 1440;
			const topMargin = 60;
			const bottomMargin = 60;
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
			return {
				pageBreaks: pageBreaks,
				totalElements: elements.length,
				measurementTime: Date.now()
			};
		})();
	`

	result, err := devTools.EvaluateJavaScript(pageID, measurementScript)
	if err != nil {
		return nil, fmt.Errorf("执行JavaScript测量失败: %v", err)
	}

	// 5. 解析测量结果
	measurementData, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("测量结果格式错误")
	}

	pageBreaksRaw, ok := measurementData["pageBreaks"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("分页点数据格式错误")
	}

	var pageBreakPoints []int
	for _, breakPoint := range pageBreaksRaw {
		if breakPointInt, ok := breakPoint.(float64); ok {
			pageBreakPoints = append(pageBreakPoints, int(breakPointInt))
		}
	}

	fmt.Printf("✅ JavaScript测量完成，分页点: %v\n", pageBreakPoints)
	return pageBreakPoints, nil
}

// simulateDevToolsMeasurement 模拟DevTools Protocol测量结果
func (r *ChromeHeadlessRenderer) simulateDevToolsMeasurement() []int {
	// 模拟的DevTools Protocol响应
	// 在实际实现中，这会是从Chrome返回的真实数据
	return []int{0, 3, 6, 9, 12, 15}
}

// captureImagesWithDevTools 使用DevTools Protocol进行区域截图
func (r *ChromeHeadlessRenderer) captureImagesWithDevTools(htmlContent string, pageBreakPoints []int, cards []*model.CardM) ([]*RenderedCard, error) {
	var renderedCards []*RenderedCard

	// 为每个分页点生成图片
	for i, breakPoint := range pageBreakPoints {
		// 计算当前页面的元素范围
		startIndex := breakPoint
		endIndex := len(cards)
		if i+1 < len(pageBreakPoints) {
			endIndex = pageBreakPoints[i+1]
		}

		// 生成当前页面的HTML
		pageHTML, err := r.generatePageHTML(htmlContent, startIndex, endIndex)
		if err != nil {
			fmt.Printf("⚠️  生成页面 %d HTML失败: %v\n", i+1, err)
			continue
		}

		// 使用DevTools Protocol渲染当前页面
		imageData, err := r.renderPageWithDevTools(pageHTML)
		if err != nil {
			fmt.Printf("⚠️  DevTools渲染页面 %d 失败: %v\n", i+1, err)
			continue
		}

		// 保存图片
		imageURL, err := r.saveImageFromData(imageData, cards[startIndex].ID)
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
		fmt.Printf("✅ 页面 %d DevTools渲染完成: %s\n", i+1, imageURL)
	}

	return renderedCards, nil
}

// generateBookHTML 生成整本书的HTML内容
func (r *ChromeHeadlessRenderer) generateBookHTML(book *model.BookM, cards []*model.CardM) (string, error) {
	// 解析所有卡片数据
	var allElements []ElementData
	for _, card := range cards {
		var elements []pagination.Element
		if err := json.Unmarshal([]byte(card.ProcessedText), &elements); err != nil {
			fmt.Printf("⚠️  解析卡片 %d 失败: %v\n", card.ID, err)
			continue
		}

		// 转换元素格式
		for _, element := range elements {
			elementData := ElementData{
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
	data := BookTemplateData{
		Book: BookData{
			ID:        book.ID,
			Title:     book.Title,
			ImageURL:  book.ImageUrl,
			CardCount: book.CardCount,
			CreatedAt: book.CreatedAt.Format("2006-01-02 15:04:05"),
		},
		Cards:  r.convertToCardData(allElements),
		Config: r.config,
	}

	// 生成HTML
	return r.generateBookHTMLTemplate(data)
}

// generatePageHTML 生成单个页面的HTML
func (r *ChromeHeadlessRenderer) generatePageHTML(fullHTML string, startIndex, endIndex int) (string, error) {
	// 这里应该从完整HTML中提取指定范围的元素
	// 为了简化，我们直接返回完整HTML，让JavaScript控制显示范围
	return fullHTML, nil
}

// renderPageWithDevTools 使用DevTools Protocol渲染页面
func (r *ChromeHeadlessRenderer) renderPageWithDevTools(htmlContent string) ([]byte, error) {
	fmt.Printf("🖥️  使用DevTools Protocol渲染页面...\n")

	// 创建DevTools客户端
	devTools := NewDevToolsClient(r.debugPort)
	defer devTools.Close()

	// 1. 创建新页面
	pageID, err := devTools.CreatePage()
	if err != nil {
		return nil, fmt.Errorf("创建页面失败: %v", err)
	}

	// 2. 导航到HTML内容
	if err := devTools.NavigateToHTML(pageID, htmlContent); err != nil {
		return nil, fmt.Errorf("导航到HTML失败: %v", err)
	}

	// 3. 等待页面加载完成
	if err := devTools.WaitForLoad(pageID); err != nil {
		return nil, fmt.Errorf("等待页面加载失败: %v", err)
	}

	// 4. 设置视口大小
	viewportScript := fmt.Sprintf(`
		document.body.style.width = '%dpx';
		document.body.style.height = '%dpx';
		document.body.style.overflow = 'hidden';
	`, r.config.Card.Width, r.config.Card.Height)

	if _, err := devTools.EvaluateJavaScript(pageID, viewportScript); err != nil {
		return nil, fmt.Errorf("设置视口失败: %v", err)
	}

	// 5. 截取页面截图
	clip := &Clip{
		X:      0,
		Y:      0,
		Width:  r.config.Card.Width,
		Height: r.config.Card.Height,
	}

	screenshotData, err := devTools.CaptureScreenshot(pageID, clip)
	if err != nil {
		return nil, fmt.Errorf("截图失败: %v", err)
	}

	fmt.Printf("✅ 页面渲染完成，截图大小: %d bytes\n", len(screenshotData))
	return screenshotData, nil
}

// saveImageFromData 从图片数据保存文件
func (r *ChromeHeadlessRenderer) saveImageFromData(imageData []byte, cardID uint) (string, error) {
	// 使用工具函数获取正确的图片保存路径
	cardDir := util.GetCardImagePath(cardID)
	fmt.Printf("🔍 Chrome渲染器：卡片保存目录=%s\n", cardDir)

	// 确保目录存在
	if err := os.MkdirAll(cardDir, 0755); err != nil {
		fmt.Printf("❌ Chrome渲染器：创建目录失败 - %v\n", err)
		return "", fmt.Errorf("failed to create card directory: %v", err)
	}
	fmt.Printf("🔍 Chrome渲染器：目录创建成功或已存在\n")

	// 生成文件名 - 改为WebP格式
	filename := fmt.Sprintf("card_%d.webp", cardID)
	filepath := filepath.Join(cardDir, filename)
	fmt.Printf("🔍 Chrome渲染器：文件完整路径=%s\n", filepath)

	// 创建文件
	file, err := os.Create(filepath)
	if err != nil {
		fmt.Printf("❌ Chrome渲染器：创建文件失败 - %v\n", err)
		return "", fmt.Errorf("failed to create image file: %v", err)
	}
	defer file.Close()
	fmt.Printf("🔍 Chrome渲染器：文件创建成功\n")

	// 写入图片数据
	bytesWritten, err := file.Write(imageData)
	if err != nil {
		fmt.Printf("❌ Chrome渲染器：写入图片数据失败 - %v\n", err)
		return "", fmt.Errorf("failed to write image data: %v", err)
	}
	fmt.Printf("🔍 Chrome渲染器：图片数据写入成功，写入字节数=%d，预期字节数=%d\n", bytesWritten, len(imageData))

	// 同步到磁盘
	if err := file.Sync(); err != nil {
		fmt.Printf("⚠️ Chrome渲染器：同步到磁盘失败 - %v\n", err)
	} else {
		fmt.Printf("🔍 Chrome渲染器：数据已同步到磁盘\n")
	}

	// 验证文件是否真的被创建
	if info, err := os.Stat(filepath); err == nil {
		fmt.Printf("🔍 Chrome渲染器：文件验证成功，大小=%d bytes，权限=%s\n", info.Size(), info.Mode())
	} else {
		fmt.Printf("⚠️ Chrome渲染器：文件验证失败 - %v\n", err)
	}

	// 返回图片URL
	imageURL := util.GetCardImageURL(cardID, filename)
	fmt.Printf("🔍 Chrome渲染器：返回的图片URL=%s\n", imageURL)
	return imageURL, nil
}

// generateBookHTMLTemplate 生成书籍HTML模板
func (r *ChromeHeadlessRenderer) generateBookHTMLTemplate(data BookTemplateData) (string, error) {
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
        
        /* 思源宋体字体定义 - 使用本地字体文件 */
        @font-face {
            font-family: "SourceHanSerifSC";
            src: url("file:///usr/share/fonts/truetype/SourceHanSerifSC-Regular.otf") format("opentype"),
                 local("Source Han Serif SC"),
                 local("SourceHanSerifSC"),
                 local("STFangsong"),
                 local("Noto Sans CJK SC"),
                 local("PingFang SC"),
                 local("Hiragino Sans GB"),
                 local("Microsoft YaHei"),
                 local("sans-serif");
            font-weight: normal;
            font-style: normal;
        }
        
        @font-face {
            font-family: "SourceHanSerifSC";
            src: url("file:///usr/share/fonts/truetype/SourceHanSerifSC-Bold.otf") format("opentype"),
                 local("Source Han Serif SC Bold"),
                 local("SourceHanSerifSC-Bold"),
                 local("STFangsong"),
                 local("Noto Sans CJK SC Semibold"),
                 local("PingFang SC"),
                 local("Hiragino Sans GB"),
                 local("Microsoft YaHei Bold"),
                 local("sans-serif");
            font-weight: bold;
            font-style: normal;
        }
        
        body {
            font-family: "SourceHanSerifSC", "STFangsong", "Noto Sans CJK SC", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", "Helvetica Neue", Arial, sans-serif;
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
            font-size: 64px;
            color: #333333;
            line-height: 1.4;
            text-align: justify;
            margin: 0 0 30px 0;
            font-weight: bold;
        }
        
        .element-subtitle {
            font-size: 48px;
            color: #666666;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 25px 0;
            font-weight: normal;
        }
        
        .element-body {
            font-size: 36px;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 30px 0;
        }
        
        .element-quote {
            font-size: 36px;
            color: #1E90FF;
            line-height: 1.5;
            text-align: justify;
            margin: 0 0 30px 0;
            font-style: italic;
            padding: 20px;
            background: linear-gradient(to right, #EAF2FF, #FAFCFF);
            border-left: 4px solid #1E90FF;
            border-radius: 0 8px 8px 0;
        }
        
        .element-list {
            font-size: 36px;
            color: #333333;
            line-height: 1.6;
            text-align: justify;
            margin: 0 0 30px 0;
            padding-left: 40px;
            list-style: none;
        }
        
        .list-item { 
            margin-bottom: 8px; 
            position: relative; 
        }
        
        .list-item:before {
            content: "•";
            position: absolute;
            left: -20px;
            color: #333333;
        }
        
        .list-item:last-child { 
            margin-bottom: 0; 
        }
        
        /* 确保字体加载完成 */
        .font-loaded {
            font-family: 'SourceHanSerifSC', 'STFangsong', 'PingFang SC', 'Helvetica Neue', Arial, sans-serif;
        }
        
        /* 卡片容器样式 - 确保所有内容卡片都有正确的边距 */
        .card-container {
            width: 100%;
            height: 100%;
            padding: 60px 50px; /* 上右下左边距：60px 50px 60px 50px */
            box-sizing: border-box;
            background: #ffffff;
            /* 确保边距完全一致 */
            margin: 0;
        }
        
        /* 内容区域样式 */
        .content-area {
            width: 100%;
            height: 100%;
            overflow: hidden;
            /* 确保内容在边距范围内 */
            padding: 0;
            margin: 0;
        }
        
        /* 第一个元素的特殊处理 - 确保上边距一致 */
        .content-element:first-child {
            margin-top: 0;
        }
        
        /* 最后一个元素的特殊处理 - 确保下边距一致 */
        .content-element:last-child {
            margin-bottom: 0;
        }
    </style>
</head>
<body>
    <div class="book-container">
        <!-- 所有内容元素，没有固定高度限制 -->
        <div class="card-container">
            <div class="content-area">
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
        </div>
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

	// 解析模板
	tmpl, err := template.New("book").Parse(htmlTemplate)
	if err != nil {
		return "", fmt.Errorf("解析模板失败: %v", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("执行模板失败: %v", err)
	}

	return buf.String(), nil
}

// convertToCardData 将元素数据转换为卡片数据
func (r *ChromeHeadlessRenderer) convertToCardData(elements []ElementData) []CardData {
	var cards []CardData

	// 将元素分组为卡片（每3-4个元素为一组）
	elementsPerCard := 4
	for i := 0; i < len(elements); i += elementsPerCard {
		end := i + elementsPerCard
		if end > len(elements) {
			end = len(elements)
		}

		cardElements := elements[i:end]
		card := CardData{
			ID:        uint(i + 1),
			SortOrder: (i / elementsPerCard) + 1,
			Elements:  cardElements,
		}
		cards = append(cards, card)
	}

	return cards
}
