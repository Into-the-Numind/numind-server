package util

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"numind-server/internal/pkg/log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// WkhtmltoimageConfig 配置选项
type WkhtmltoimageConfig struct {
	Width   int           `json:"width"`   // 图片宽度
	Height  int           `json:"height"`  // 图片高度
	Quality int           `json:"quality"` // 图片质量 (1-100)
	Format  string        `json:"format"`  // 图片格式 (webp, png, jpg)
	Zoom    float64       `json:"zoom"`    // 缩放比例
	Timeout time.Duration `json:"timeout"` // 超时时间
}

// DefaultConfig 默认配置
func DefaultConfig() *WkhtmltoimageConfig {
	return &WkhtmltoimageConfig{
		Width:   1080,
		Height:  1440,
		Quality: 85,
		Format:  "webp",
		Zoom:    1.0,
		Timeout: 30 * time.Second,
	}
}

// WkhtmltoimageRenderer HTML到图片渲染器
type WkhtmltoimageRenderer struct {
	config *WkhtmltoimageConfig
}

// NewWkhtmltoimageRenderer 创建新的渲染器
func NewWkhtmltoimageRenderer(config *WkhtmltoimageConfig) *WkhtmltoimageRenderer {
	if config == nil {
		config = DefaultConfig()
	}
	return &WkhtmltoimageRenderer{
		config: config,
	}
}

// RenderHTMLToImage 将HTML转换为图片
func (w *WkhtmltoimageRenderer) RenderHTMLToImage(ctx context.Context, htmlContent, outputPath string) error {
	// 检查是否使用外部wkhtmltoimage二进制文件
	if w.isExternalWkhtmltoimageAvailable() {
		return w.renderWithExternalBinary(ctx, htmlContent, outputPath)
	}

	log.C(ctx).Infow("使用内置的Go实现渲染", "output_path", outputPath)
	// 使用内置的Go实现
	return w.renderWithGoImplementation(ctx, htmlContent, outputPath)
}

// isExternalWkhtmltoimageAvailable 检查外部wkhtmltoimage是否可用
func (w *WkhtmltoimageRenderer) isExternalWkhtmltoimageAvailable() bool {
	cmd := exec.Command("wkhtmltoimage", "--version")
	return cmd.Run() == nil
}

// renderWithExternalBinary 使用外部二进制文件渲染
func (w *WkhtmltoimageRenderer) renderWithExternalBinary(ctx context.Context, htmlContent, outputPath string) error {
	// 创建临时HTML文件
	tempDir := filepath.Dir(outputPath)
	tempHTMLFile := filepath.Join(tempDir, fmt.Sprintf("temp_%d.html", time.Now().UnixNano()))

	if err := os.WriteFile(tempHTMLFile, []byte(htmlContent), 0644); err != nil {
		return fmt.Errorf("failed to create temporary HTML file: %v", err)
	}
	defer os.Remove(tempHTMLFile) // 清理临时文件

	// 构建命令参数
	args := []string{
		"--quality", fmt.Sprintf("%d", w.config.Quality),
		"--format", w.config.Format,
		"--width", fmt.Sprintf("%d", w.config.Width),
		"--height", fmt.Sprintf("%d", w.config.Height),
		"--enable-local-file-access",
		"--disable-smart-width",
		"--disable-smart-height",
		"--zoom", fmt.Sprintf("%.1f", w.config.Zoom),
		"--javascript-delay", "1000",
		"--no-stop-slow-scripts",
		"--encoding", "UTF-8",
		tempHTMLFile,
		outputPath,
	}

	// 执行命令
	cmd := exec.CommandContext(ctx, "wkhtmltoimage", args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wkhtmltoimage command failed: %v", err)
	}

	return nil
}

// renderWithGoImplementation 使用Go实现渲染
func (w *WkhtmltoimageRenderer) renderWithGoImplementation(ctx context.Context, htmlContent, outputPath string) error {
	// 确保输出目录存在
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %v", err)
	}

	// 创建临时HTML文件
	tempDir := filepath.Dir(outputPath)
	tempHTMLFile := filepath.Join(tempDir, fmt.Sprintf("temp_%d.html", time.Now().UnixNano()))

	if err := os.WriteFile(tempHTMLFile, []byte(htmlContent), 0644); err != nil {
		return fmt.Errorf("failed to create temporary HTML file: %v", err)
	}
	defer os.Remove(tempHTMLFile)

	// 使用内置的Go实现进行渲染
	// 这里我们使用一个简化的实现，实际项目中可以集成更完整的HTML渲染引擎
	return w.renderWithSimpleGoImplementation(ctx, tempHTMLFile, outputPath)
}

// renderWithSimpleGoImplementation 使用简化的Go实现
func (w *WkhtmltoimageRenderer) renderWithSimpleGoImplementation(ctx context.Context, htmlFile, outputPath string) error {
	// 使用chromedp进行真正的HTML渲染
	return w.renderWithChromedp(ctx, htmlFile, outputPath)
}

// renderWithChromedp 使用chromedp进行HTML渲染
func (w *WkhtmltoimageRenderer) renderWithChromedp(ctx context.Context, htmlFile, outputPath string) error {
	// 创建Chrome选项 - 容器环境优化（重点：减少进程数）
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-setuid-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-web-security", true),
		chromedp.Flag("window-size", fmt.Sprintf("%d,%d", w.config.Width, w.config.Height)),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-plugins", true),
		chromedp.Flag("disable-images", false),
		chromedp.Flag("disable-javascript", false),

		// 容器环境特定优化
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-ipc-flooding-protection", true),
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

		// 字体渲染优化
		chromedp.Flag("font-render-hinting", "none"),
		chromedp.Flag("disable-font-subpixel-positioning", true),

		// 内存和进程优化（关键：减少子进程数量）
		chromedp.Flag("max_old_space_size", "2048"), // 降低内存限制
		chromedp.Flag("memory-pressure-off", true),
		chromedp.Flag("renderer-process-limit", "1"), // 限制渲染进程数为1
		chromedp.Flag("disable-breakpad", true),      // 禁用崩溃报告
		chromedp.Flag("disable-client-side-phishing-detection", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-zygote", true), // 禁用zygote进程（减少fork）
		chromedp.Flag("disable-features", "TranslateUI,BlinkGenPropertyTrees"),
	)

	// 创建Chrome实例
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocCancel()

	// 创建Chrome任务上下文
	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()

	// 设置超时
	renderCtx, cancel := context.WithTimeout(taskCtx, w.config.Timeout)
	defer cancel()

	// 获取绝对路径
	absPath, err := filepath.Abs(htmlFile)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %v", err)
	}

	fileURL := "file://" + absPath
	var imageData []byte

	// 执行渲染任务
	err = chromedp.Run(renderCtx,
		chromedp.EmulateViewport(int64(w.config.Width), int64(w.config.Height)),
		chromedp.Navigate(fileURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(1*time.Second), // 减少初始等待时间
		chromedp.ActionFunc(func(ctx context.Context) error {
			// 等待字体加载 - 容器环境优化
			_ = chromedp.Evaluate(`document.fonts.ready`, nil).Do(ctx)

			// 检查思源宋体是否加载成功
			var fontLoaded bool
			_ = chromedp.Evaluate(`
				document.fonts.check('16px "SourceHanSerifSC"') ||
				document.fonts.check('16px "STFangsong"') ||
				document.fonts.check('16px "Noto Sans CJK SC"')
			`, &fontLoaded).Do(ctx)

			// 根据字体加载状态动态等待 - 优化性能
			// 如果字体已加载，减少等待时间；否则等待更长时间
			if fontLoaded {
				time.Sleep(2 * time.Second) // 字体已加载，短等待
			} else {
				time.Sleep(4 * time.Second) // 字体未加载，需要更多时间
			}

			// 强制重绘页面
			_ = chromedp.Evaluate(`document.body.style.display='none';document.body.offsetHeight;document.body.style.display=''`, nil).Do(ctx)

			// 截图
			var screenshotErr error
			if strings.ToLower(w.config.Format) == "webp" {
				imageData, screenshotErr = page.CaptureScreenshot().
					WithFormat(page.CaptureScreenshotFormatWebp).
					WithQuality(int64(w.config.Quality)).
					Do(ctx)
			} else {
				imageData, screenshotErr = page.CaptureScreenshot().
					WithFormat(page.CaptureScreenshotFormatPng).
					WithQuality(90).
					Do(ctx)
			}

			if screenshotErr != nil {
				return fmt.Errorf("screenshot failed: %v", screenshotErr)
			}

			return nil
		}),
	)

	if err != nil {
		return fmt.Errorf("chromedp rendering failed: %v", err)
	}

	// 保存图片文件
	if err := os.WriteFile(outputPath, imageData, 0644); err != nil {
		return fmt.Errorf("failed to save image: %v", err)
	}

	return nil
}

// RenderHTMLStringToImage 直接渲染HTML字符串到图片
func (w *WkhtmltoimageRenderer) RenderHTMLStringToImage(ctx context.Context, htmlContent, outputPath string) error {
	return w.RenderHTMLToImage(ctx, htmlContent, outputPath)
}

// RenderHTMLFileToImage 渲染HTML文件到图片
func (w *WkhtmltoimageRenderer) RenderHTMLFileToImage(ctx context.Context, htmlFilePath, outputPath string) error {
	// 读取HTML文件内容
	htmlContent, err := os.ReadFile(htmlFilePath)
	if err != nil {
		return fmt.Errorf("failed to read HTML file: %v", err)
	}

	return w.RenderHTMLToImage(ctx, string(htmlContent), outputPath)
}

// RenderHTMLToBytes 渲染HTML到字节数组
func (w *WkhtmltoimageRenderer) RenderHTMLToBytes(ctx context.Context, htmlContent string) ([]byte, error) {
	// 创建临时输出文件
	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("wkhtmltoimage_%d.%s", time.Now().UnixNano(), w.config.Format))
	defer os.Remove(tempFile)

	if err := w.RenderHTMLToImage(ctx, htmlContent, tempFile); err != nil {
		return nil, err
	}

	// 读取生成的图片文件
	return os.ReadFile(tempFile)
}

// RenderHTMLToReader 渲染HTML到Reader
func (w *WkhtmltoimageRenderer) RenderHTMLToReader(ctx context.Context, htmlContent string) (io.Reader, error) {
	data, err := w.RenderHTMLToBytes(ctx, htmlContent)
	if err != nil {
		return nil, err
	}

	return bytes.NewReader(data), nil
}

// SetConfig 更新配置
func (w *WkhtmltoimageRenderer) SetConfig(config *WkhtmltoimageConfig) {
	if config != nil {
		w.config = config
	}
}

// GetConfig 获取当前配置
func (w *WkhtmltoimageRenderer) GetConfig() *WkhtmltoimageConfig {
	return w.config
}

// IsAvailable 检查渲染器是否可用
func (w *WkhtmltoimageRenderer) IsAvailable() bool {
	// 检查外部二进制文件
	if w.isExternalWkhtmltoimageAvailable() {
		return true
	}

	// 检查Go实现是否可用（这里总是返回true，因为我们的实现总是可用的）
	return true
}

// GetVersion 获取版本信息
func (w *WkhtmltoimageRenderer) GetVersion() string {
	if w.isExternalWkhtmltoimageAvailable() {
		cmd := exec.Command("wkhtmltoimage", "--version")
		if output, err := cmd.Output(); err == nil {
			return strings.TrimSpace(string(output))
		}
	}

	return "go-wkhtmltoimage v1.0.0 (Go implementation)"
}
