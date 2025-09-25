package card

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"

	"bytes"
	"image"
	"image/png"
	"os/exec"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// SimpleHeadlessRenderer 简化版无头浏览器渲染器
type SimpleHeadlessRenderer struct {
	config             *pagination.PaginationConfig
	background         string
	templateBackground string
}

// 确保SimpleHeadlessRenderer实现了RendererInterface接口
var _ RendererInterface = (*SimpleRenderer)(nil)

// NewSimpleHeadlessRenderer 创建新的简化版渲染器
func NewSimpleHeadlessRenderer(config *pagination.PaginationConfig) *SimpleHeadlessRenderer {
	return &SimpleHeadlessRenderer{
		config: config,
	}
}

// SetBackground 设置背景
func (r *SimpleHeadlessRenderer) SetBackground(background string) {
	r.background = background
}

// SetTemplateBackground 设置模板背景
func (r *SimpleHeadlessRenderer) SetTemplateBackground(background string) error {
	r.templateBackground = background
	return nil
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

	// 优先使用模板背景，如果没有则使用普通背景
	var bgStyle string
	if r.templateBackground != "" {
		// 使用模板背景，确保完全覆盖 - 使用正确的URL处理逻辑
		bgStyle = formatBackgroundStyle(r.templateBackground)
		fmt.Printf("🔍 使用模板背景: %s\n", r.templateBackground)
	} else if r.background != "" {
		// 使用普通背景
		bgStyle = formatBackgroundStyle(r.background)
		fmt.Printf("🔍 使用普通背景: %s\n", r.background)
	} else {
		// 默认白色背景
		bgStyle = "background: #ffffff;"
		fmt.Printf("🔍 使用默认白色背景\n")
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
            padding: 60px 50px; /* 上右下左边距：60px 50px 60px 50px */
            font-family: "SourceHanSerifSC", "STFangsong", -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Noto Sans CJK SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
            %s
            color: #333333;
            line-height: 1.6;
            width: %[1]dpx;
            height: %[2]dpx;
            box-sizing: border-box;
            background-clip: border-box;
            overflow: hidden;
            /* 确保背景完全覆盖 */
            background-size: cover !important;
            background-position: center center !important;
            background-repeat: no-repeat !important;
        }
        
        /* 卡片容器样式 - 确保所有内容卡片都有正确的边距 */
        .card-container {
            width: 100%%;
            height: 100%%;
            padding: 0; /* 重置内边距，因为body已经有了 */
            box-sizing: border-box;
            /* 确保边距完全一致 */
            margin: 0;
            /* 继承body的背景 */
            background: inherit;
        }
        
        /* 内容区域样式 */
        .content-area {
            width: 100%%;
            height: 100%%;
            overflow: hidden;
            /* 确保内容在边距范围内 */
            padding: 0;
            margin: 0;
            /* 继承背景 */
            background: inherit;
        }
        
        /* 第一个元素的特殊处理 - 确保上边距一致 */
        .content-element:first-child {
            margin-top: 0;
        }
        
        /* 最后一个元素的特殊处理 - 确保下边距一致 */
        .content-element:last-child {
            margin-bottom: 0;
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
            margin-bottom: 30px;
            text-align: justify;
            line-height: 1.6;
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
        .quote {
            font-size: 36px;
            color: #1E90FF;
            margin-bottom: 30px;
            text-align: justify;
            line-height: 1.5;
            padding: 20px;
            background: linear-gradient(to right, #EAF2FF, #FAFCFF);
            border-left: 4px solid #1E90FF;
            border-radius: 0 8px 8px 0;
            font-style: italic;
        }
    </style>
</head>
<body>
    <div class="card-container">
        <div class="content-area">`, r.config.Card.Width, r.config.Card.Height, bgStyle, r.config.Card.Width, r.config.Card.Height)

	fmt.Printf("🔍 调试：HTML头部生成完成，长度=%d bytes\n", len(html))

	// 生成元素内容
	for i, element := range elements {
		fmt.Printf("🔍 调试：处理元素 %d，类型=%s，内容长度=%d\n", i, element.Type, len(fmt.Sprintf("%v", element.Content)))

		switch element.Type {
		case pagination.ElementTypeTitle:
			html += fmt.Sprintf(`<div class="title">%s</div>`, element.Content)
		case pagination.ElementTypeSubtitle:
			html += fmt.Sprintf(`<div class="subtitle">%s</div>`, element.Content)
		case pagination.ElementTypeBody:
			html += fmt.Sprintf(`<div class="body">%s</div>`, element.Content)
		case pagination.ElementTypeList:
			if items, ok := element.Content.([]string); ok {
				html += `<div class="list">`
				for _, item := range items {
					html += fmt.Sprintf(`<div class="list-item">%s</div>`, item)
				}
				html += `</div>`
			} else {
				html += fmt.Sprintf(`<div class="body">%s</div>`, element.Content)
			}
		case pagination.ElementTypeQuote:
			html += fmt.Sprintf(`<div class="quote">%s</div>`, element.Content)
		default:
			html += fmt.Sprintf(`<div class="body">%s</div>`, element.Content)
		}
	}

	html += `</div></div></body></html>`
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

	// 创建Chrome选项 - 容器环境优化
	fmt.Printf("🔍 调试：创建Chrome选项...\n")
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("disable-features", "VizDisplayCompositor,Translate,BackForwardCache,AcceptCHFrame,MediaRouter,OptimizationHints,AudioServiceOutOfProcess"),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", r.config.Card.Width, r.config.Card.Height)),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-plugins", true),
		chromedp.Flag("disable-images", false),     // 保持图片渲染
		chromedp.Flag("disable-javascript", false), // 保持JS支持
		// 字体渲染优化 - 容器环境
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("disable-font-subpixel-positioning", true),
		// 容器环境特定优化
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-ipc-flooding-protection", true),
		chromedp.Flag("max_old_space_size", "4096"), // 增加内存限制
		chromedp.Flag("memory-pressure-off", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("disable-logging", true),
		chromedp.Flag("disable-breakpad", true),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.Flag("disable-prompt-on-repost", true),
		chromedp.Flag("disable-domain-reliability", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("disable-field-trial-config", true),
		chromedp.Flag("disable-background-mode", true),
		chromedp.Flag("disable-software-rasterizer", true),
		chromedp.Flag("disable-canvas-aa", true),
		chromedp.Flag("disable-2d-canvas-clip-aa", true),
		chromedp.Flag("disable-gl-drawing-for-tests", true),
		// 字体加载优化
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

	// 设置超时 - 容器环境需要更长时间
	ctx, cancel := context.WithTimeout(taskCtx, 120*time.Second)
	defer cancel()
	fmt.Printf("🔍 调试：设置120秒超时（容器环境优化）\n")

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

			// 等待字体加载 - 增加超时时间
			if err := chromedp.Evaluate(`document.fonts.ready`, nil).Do(ctx); err == nil {
				fmt.Printf("🔍 调试：字体加载完成\n")
			}

			// 额外等待时间确保字体完全加载
			time.Sleep(3 * time.Second)

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
		// 检查是否为超时错误，如果是则重试
		if strings.Contains(err.Error(), "context deadline exceeded") || strings.Contains(err.Error(), "timeout") {
			fmt.Printf("🔄 检测到超时错误，尝试重试...\n")
			return r.retryRenderWithHeadlessBrowser(htmlContent, 1)
		}
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	fmt.Printf("🔍 调试：渲染任务执行成功\n")
	fmt.Printf("🔍 调试：生成的图片大小: %d bytes\n", len(imageData))
	return imageData, nil
}

// retryRenderWithHeadlessBrowser 重试渲染（最多3次）
func (r *SimpleHeadlessRenderer) retryRenderWithHeadlessBrowser(htmlContent string, attempt int) ([]byte, error) {
	const maxRetries = 3
	if attempt > maxRetries {
		fmt.Printf("❌ 重试次数已达上限 (%d次)\n", maxRetries)
		return nil, fmt.Errorf("max retries exceeded")
	}

	fmt.Printf("🔄 第 %d 次重试渲染 (共%d次机会)...\n", attempt, maxRetries)

	// 增加重试间隔
	time.Sleep(time.Duration(attempt) * 5 * time.Second)

	// 重新尝试渲染
	imageData, err := r.renderWithHeadlessBrowser(htmlContent)
	if err != nil {
		fmt.Printf("❌ 第 %d 次重试失败 - %v\n", attempt, err)
		// 递归重试
		return r.retryRenderWithHeadlessBrowser(htmlContent, attempt+1)
	}

	fmt.Printf("✅ 第 %d 次重试成功\n", attempt)
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

	// 生成文件名 - 改为webp格式
	filename := fmt.Sprintf("card_%d.webp", cardID)
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

	// 将PNG数据转换为webp格式
	if err := r.convertToWebP(imageData, file); err != nil {
		fmt.Printf("❌ 调试：webp转换失败 - %v\n", err)
		return "", fmt.Errorf("failed to convert to webp: %v", err)
	}
	fmt.Printf("🔍 调试：webp转换成功\n")

	// 验证文件是否真的被创建
	if info, err := os.Stat(filepath); err != nil {
		fmt.Printf("❌ 调试：文件验证失败 - %v\n", err)
		return "", fmt.Errorf("failed to verify created file: %v", err)
	} else {
		fmt.Printf("🔍 调试：文件创建验证成功，大小=%d bytes\n", info.Size())
	}

	// 构建本地URL
	imageURL := util.GetCardImageURL(cardID, filename)

	// 备份上传到腾讯云 COS（忽略错误，失败则使用本地路径）
	if util.IsCOSEnabled() {
		objectKey := path.Join("card", fmt.Sprintf("%d", cardID), filename)
		if data, err := os.ReadFile(filepath); err == nil {
			if cosURL, err := util.UploadBytesToCOS(context.Background(), objectKey, "image/webp", data); err == nil && cosURL != "" {
				// 尝试生成短期签名 URL 以支持私有桶读取
				if signed, err := util.GenerateSignedURL(context.Background(), objectKey, 600); err == nil && signed != "" {
					imageURL = signed
				} else {
					imageURL = cosURL
				}
			}
		}
	}

	fmt.Printf("🔍 调试：返回的图片URL=%s\n", imageURL)
	return imageURL, nil
}

// convertToWebP 将图片数据转换为webp格式
func (r *SimpleHeadlessRenderer) convertToWebP(imageData []byte, outputFile *os.File) error {
	// 解码图片数据
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return fmt.Errorf("failed to decode image data: %v", err)
	}

	// 使用cwebp命令行工具转换为webp格式，确保高质量输出
	// 创建临时PNG文件
	tempPNG := fmt.Sprintf("/tmp/temp_%d.png", time.Now().UnixNano())
	tempFile, err := os.Create(tempPNG)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %v", err)
	}

	// 将图片保存为临时PNG文件
	if err := png.Encode(tempFile, img); err != nil {
		tempFile.Close()
		os.Remove(tempPNG)
		return fmt.Errorf("failed to encode temp PNG: %v", err)
	}

	// 关闭临时文件，确保数据写入
	tempFile.Close()

	// 验证临时PNG文件是否创建成功
	if info, err := os.Stat(tempPNG); err != nil || info.Size() == 0 {
		os.Remove(tempPNG)
		return fmt.Errorf("temp PNG file creation failed or is empty: %v", err)
	}

	// 使用cwebp转换，设置高质量参数
	// 直接输出到目标文件路径
	outputPath := outputFile.Name()
	cmd := exec.Command("cwebp", "-q", "95", "-m", "6", "-af", "-f", "50", "-sharpness", "0", tempPNG, "-o", outputPath)

	// 捕获命令输出用于调试
	output, err := cmd.CombinedOutput()
	if err != nil {
		os.Remove(tempPNG)
		return fmt.Errorf("failed to convert to webp: %v, output: %s", err, string(output))
	}

	// 清理临时文件
	os.Remove(tempPNG)

	return nil
}
