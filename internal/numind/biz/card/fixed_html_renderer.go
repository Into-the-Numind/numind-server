package card

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// FixedHTMLRenderer 修复版HTML渲染器
type FixedHTMLRenderer struct {
	config  *pagination.PaginationConfig
	quality int
	timeout time.Duration
}

// NewFixedHTMLRenderer 创建新的修复版HTML渲染器
func NewFixedHTMLRenderer(config *pagination.PaginationConfig) *FixedHTMLRenderer {
	return &FixedHTMLRenderer{
		config:  config,
		quality: 85,
		timeout: 30 * time.Second,
	}
}

// SetQuality 设置WebP质量
func (r *FixedHTMLRenderer) SetQuality(quality int) {
	if quality < 1 {
		quality = 1
	} else if quality > 100 {
		quality = 100
	}
	r.quality = quality
}

// RenderHTMLFileToWebP 将HTML文件渲染为WebP图片
func (r *FixedHTMLRenderer) RenderHTMLFileToWebP(htmlFilePath string, outputPath string) error {
	log.C(context.Background()).Infow("开始修复版HTML渲染",
		"html_file", htmlFilePath,
		"output_path", outputPath)

	// 检查HTML文件是否存在
	if _, err := os.Stat(htmlFilePath); err != nil {
		return fmt.Errorf("HTML文件不存在: %v", err)
	}

	// 读取HTML内容
	htmlContent, err := os.ReadFile(htmlFilePath)
	if err != nil {
		return fmt.Errorf("读取HTML文件失败: %v", err)
	}

	// 修复HTML内容
	fixedHTML := r.fixHTMLContent(string(htmlContent))

	// 创建输出目录
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %v", err)
	}

	// 渲染修复后的HTML
	imageData, err := r.renderFixedHTML(fixedHTML)
	if err != nil {
		return fmt.Errorf("渲染HTML失败: %v", err)
	}

	// 保存WebP文件
	if err := os.WriteFile(outputPath, imageData, 0644); err != nil {
		return fmt.Errorf("保存WebP文件失败: %v", err)
	}

	log.C(context.Background()).Infow("修复版HTML渲染完成",
		"output_path", outputPath,
		"image_size", len(imageData))

	return nil
}

// fixHTMLContent 修复HTML内容
func (r *FixedHTMLRenderer) fixHTMLContent(htmlContent string) string {
	// 修复CSS样式问题
	fixedCSS := `
		body {
			overflow: hidden !important;
			width: 1080px !important;
			height: 1440px !important;
			margin: 0 !important;
			padding: 0 !important;
		}
		.markdown-card-container {
			overflow: hidden !important;
			width: 1080px !important;
			height: 1440px !important;
		}
		.markdown-content {
			overflow: hidden !important;
		}
	`

	// 在</style>标签前插入修复的CSS
	htmlContent = fmt.Sprintf("%s\n%s\n</style>",
		htmlContent[:len(htmlContent)-8], // 移除</style>
		fixedCSS)

	return htmlContent
}

// renderFixedHTML 渲染修复后的HTML
func (r *FixedHTMLRenderer) renderFixedHTML(htmlContent string) ([]byte, error) {
	// 创建临时HTML文件
	tempFile, err := os.CreateTemp("", "fixed_html_*.html")
	if err != nil {
		return nil, fmt.Errorf("创建临时文件失败: %v", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// 写入修复后的HTML内容
	if _, err := tempFile.WriteString(htmlContent); err != nil {
		return nil, fmt.Errorf("写入HTML内容失败: %v", err)
	}

	// 获取绝对路径
	absPath, err := filepath.Abs(tempFile.Name())
	if err != nil {
		return nil, fmt.Errorf("获取绝对路径失败: %v", err)
	}

	fileURL := "file://" + absPath
	log.C(context.Background()).Infow("临时HTML文件准备完成",
		"temp_file", tempFile.Name(),
		"abs_path", absPath)

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
		chromedp.Flag("disable-images", false),
		chromedp.Flag("disable-javascript", false),
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("disable-font-subpixel-positioning", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
	)

	// 创建Chrome实例
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	// 创建Chrome任务
	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 设置超时
	ctx, cancel := context.WithTimeout(taskCtx, r.timeout)
	defer cancel()

	var imageData []byte

	// 执行渲染任务
	err = chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(r.config.Card.Width), int64(r.config.Card.Height)),
		chromedp.Navigate(fileURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(2*time.Second),
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.C(context.Background()).Infow("页面加载完成，开始截图")

			// 等待字体加载
			if err := chromedp.Evaluate(`document.fonts.ready`, nil).Do(ctx); err == nil {
				log.C(context.Background()).Infow("字体加载完成")
			}

			// 强制重绘页面
			if err := chromedp.Evaluate(`document.body.style.display='none';document.body.offsetHeight;document.body.style.display=''`, nil).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面重绘完成")
			}

			// 检查页面内容
			var bodyText string
			if err := chromedp.Text("body", &bodyText).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面内容检查",
					"body_length", len(bodyText))
			}

			// 检查页面高度
			var pageHeight float64
			if err := chromedp.Evaluate(`document.body.scrollHeight`, &pageHeight).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面高度检查", "page_height", pageHeight)
			}

			// 截图
			var screenshotErr error
			imageData, screenshotErr = page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithQuality(90).
				Do(ctx)

			if screenshotErr != nil {
				log.C(context.Background()).Errorw("PNG截图失败，尝试WebP", "error", screenshotErr)

				// 如果PNG失败，尝试WebP格式
				imageData, screenshotErr = page.CaptureScreenshot().
					WithFormat(page.CaptureScreenshotFormatWebp).
					WithQuality(int64(r.quality)).
					Do(ctx)

				if screenshotErr != nil {
					return fmt.Errorf("both PNG and WebP screenshot failed: %v", screenshotErr)
				}
			}

			log.C(context.Background()).Infow("截图完成",
				"image_size", len(imageData))

			return nil
		}),
	)

	if err != nil {
		log.C(context.Background()).Errorw("无头浏览器渲染失败", "error", err)
		return nil, fmt.Errorf("failed to render with headless browser: %v", err)
	}

	return imageData, nil
}

// RenderCardToImage 实现RendererInterface接口
func (r *FixedHTMLRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	// 创建输出路径
	outputPath := filepath.Join("res", "upload", "card", fmt.Sprintf("card_%d.webp", card.ID))

	// 如果card.ProcessedText包含HTML文件路径，渲染该文件
	if card.ProcessedText != "" {
		// 检查是否是文件路径
		if _, err := os.Stat(card.ProcessedText); err == nil {
			// 是文件路径
			err := r.RenderHTMLFileToWebP(card.ProcessedText, outputPath)
			if err != nil {
				return nil, err
			}
		} else {
			// 是HTML内容
			imageData, err := r.renderFixedHTML(card.ProcessedText)
			if err != nil {
				return nil, err
			}

			// 保存文件
			if err := os.WriteFile(outputPath, imageData, 0644); err != nil {
				return nil, fmt.Errorf("保存WebP文件失败: %v", err)
			}
		}
	} else {
		return nil, fmt.Errorf("no HTML content or file path provided")
	}

	return &RenderedCard{
		CardID:    card.ID,
		ImageURL:  fmt.Sprintf("/res/upload/card/card_%d.webp", card.ID),
		Width:     r.config.Card.Width,
		Height:    r.config.Card.Height,
		SortOrder: card.SortOrder,
	}, nil
}

// SetConfig 更新配置
func (r *FixedHTMLRenderer) SetConfig(config *pagination.PaginationConfig) {
	r.config = config
}

// GetConfig 获取当前配置
func (r *FixedHTMLRenderer) GetConfig() *pagination.PaginationConfig {
	return r.config
}

// IsAvailable 检查渲染器是否可用
func (r *FixedHTMLRenderer) IsAvailable() bool {
	// 检查Chrome是否可用
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)

	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// 设置短超时进行测试
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return chromedp.Run(testCtx, chromedp.Navigate("data:text/html,<html><body>test</body></html>")) == nil
}

// GetVersion 获取版本信息
func (r *FixedHTMLRenderer) GetVersion() string {
	return "Fixed-HTML-Renderer v1.0.0"
}
