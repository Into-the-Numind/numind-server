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

// HTMLToWebPRenderer HTML到WebP渲染器
type HTMLToWebPRenderer struct {
	config  *pagination.PaginationConfig
	quality int
	timeout time.Duration
}

// NewHTMLToWebPRenderer 创建新的HTML到WebP渲染器
func NewHTMLToWebPRenderer(config *pagination.PaginationConfig) *HTMLToWebPRenderer {
	return &HTMLToWebPRenderer{
		config:  config,
		quality: 85,
		timeout: 30 * time.Second,
	}
}

// SetQuality 设置WebP质量
func (r *HTMLToWebPRenderer) SetQuality(quality int) {
	if quality < 1 {
		quality = 1
	} else if quality > 100 {
		quality = 100
	}
	r.quality = quality
}

// SetTimeout 设置超时时间
func (r *HTMLToWebPRenderer) SetTimeout(timeout time.Duration) {
	r.timeout = timeout
}

// RenderHTMLFileToWebP 将HTML文件渲染为WebP图片
func (r *HTMLToWebPRenderer) RenderHTMLFileToWebP(htmlFilePath string, outputPath string) error {
	log.C(context.Background()).Infow("开始渲染HTML文件为WebP",
		"html_file", htmlFilePath,
		"output_path", outputPath,
		"quality", r.quality)

	// 检查HTML文件是否存在
	if _, err := os.Stat(htmlFilePath); err != nil {
		return fmt.Errorf("HTML文件不存在: %v", err)
	}

	// 获取HTML文件的绝对路径
	absPath, err := filepath.Abs(htmlFilePath)
	if err != nil {
		return fmt.Errorf("获取HTML文件绝对路径失败: %v", err)
	}

	fileURL := "file://" + absPath
	log.C(context.Background()).Infow("HTML文件路径准备完成",
		"abs_path", absPath,
		"file_url", fileURL)

	// 创建输出目录
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %v", err)
	}

	// 渲染HTML为WebP
	imageData, err := r.renderHTMLToWebP(fileURL)
	if err != nil {
		return fmt.Errorf("渲染HTML失败: %v", err)
	}

	// 保存WebP文件
	if err := os.WriteFile(outputPath, imageData, 0644); err != nil {
		return fmt.Errorf("保存WebP文件失败: %v", err)
	}

	log.C(context.Background()).Infow("HTML到WebP渲染完成",
		"output_path", outputPath,
		"image_size", len(imageData))

	return nil
}

// RenderHTMLContentToWebP 将HTML内容渲染为WebP图片
func (r *HTMLToWebPRenderer) RenderHTMLContentToWebP(htmlContent string, outputPath string) error {
	log.C(context.Background()).Infow("开始渲染HTML内容为WebP",
		"html_length", len(htmlContent),
		"output_path", outputPath,
		"quality", r.quality)

	// 创建临时HTML文件
	tempFile, err := os.CreateTemp("", "html_to_webp_*.html")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %v", err)
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// 写入HTML内容
	if _, err := tempFile.WriteString(htmlContent); err != nil {
		return fmt.Errorf("写入HTML内容失败: %v", err)
	}

	// 获取临时文件的绝对路径
	absPath, err := filepath.Abs(tempFile.Name())
	if err != nil {
		return fmt.Errorf("获取临时文件绝对路径失败: %v", err)
	}

	fileURL := "file://" + absPath
	log.C(context.Background()).Infow("临时HTML文件准备完成",
		"temp_file", tempFile.Name(),
		"abs_path", absPath,
		"file_url", fileURL)

	// 创建输出目录
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %v", err)
	}

	// 渲染HTML为WebP
	imageData, err := r.renderHTMLToWebP(fileURL)
	if err != nil {
		return fmt.Errorf("渲染HTML失败: %v", err)
	}

	// 保存WebP文件
	if err := os.WriteFile(outputPath, imageData, 0644); err != nil {
		return fmt.Errorf("保存WebP文件失败: %v", err)
	}

	log.C(context.Background()).Infow("HTML内容到WebP渲染完成",
		"output_path", outputPath,
		"image_size", len(imageData))

	return nil
}

// renderHTMLToWebP 使用无头浏览器渲染HTML为WebP
func (r *HTMLToWebPRenderer) renderHTMLToWebP(fileURL string) ([]byte, error) {
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
	err := chromedp.Run(ctx,
		chromedp.EmulateViewport(int64(r.config.Card.Width), int64(r.config.Card.Height)),
		chromedp.Navigate(fileURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(3*time.Second), // 等待渲染完成
		chromedp.ActionFunc(func(ctx context.Context) error {
			log.C(context.Background()).Infow("页面加载完成，开始截图")

			// 调试：检查页面内容
			var bodyText string
			if err := chromedp.Text("body", &bodyText).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面内容检查",
					"body_length", len(bodyText))
				if len(bodyText) > 200 {
					log.C(context.Background()).Infow("页面内容预览",
						"preview", bodyText[:200])
				}
			} else {
				log.C(context.Background()).Warnw("获取页面内容失败", "error", err)
			}

			// 检查页面是否有内容
			var hasContent bool
			if err := chromedp.Evaluate(`document.body.children.length > 0`, &hasContent).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面内容检查", "has_content", hasContent)
			}

			// 检查页面高度
			var pageHeight float64
			if err := chromedp.Evaluate(`document.body.scrollHeight`, &pageHeight).Do(ctx); err == nil {
				log.C(context.Background()).Infow("页面高度检查", "page_height", pageHeight)
			}

			// 尝试截图
			var screenshotErr error

			// 首先尝试PNG格式
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
				"image_size", len(imageData),
				"format", "PNG/WebP")

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
func (r *HTMLToWebPRenderer) RenderCardToImage(card *model.CardM) (*RenderedCard, error) {
	// 这个方法用于兼容现有的渲染器接口
	// 但HTMLToWebPRenderer主要用于直接渲染HTML文件或内容

	// 创建输出路径
	outputPath := filepath.Join("res", "upload", "card", fmt.Sprintf("card_%d.webp", card.ID))

	// 这里需要根据实际情况处理
	// 如果card.ProcessedText包含HTML内容，直接渲染
	// 如果包含文件路径，渲染文件

	if card.ProcessedText != "" {
		err := r.RenderHTMLContentToWebP(card.ProcessedText, outputPath)
		if err != nil {
			return nil, err
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
func (r *HTMLToWebPRenderer) SetConfig(config *pagination.PaginationConfig) {
	r.config = config
}

// GetConfig 获取当前配置
func (r *HTMLToWebPRenderer) GetConfig() *pagination.PaginationConfig {
	return r.config
}

// IsAvailable 检查渲染器是否可用
func (r *HTMLToWebPRenderer) IsAvailable() bool {
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
func (r *HTMLToWebPRenderer) GetVersion() string {
	return "HTML-To-WebP-Renderer v1.0.0"
}
