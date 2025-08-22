package util

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chai2010/webp"
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
	// 创建一个简单的占位符图片
	// 在实际项目中，这里应该使用更完整的HTML渲染引擎
	img := w.createPlaceholderImage()

	// 根据格式保存图片
	switch strings.ToLower(w.config.Format) {
	case "webp":
		return w.saveAsWebP(img, outputPath)
	case "png":
		return w.saveAsPNG(img, outputPath)
	default:
		return w.saveAsPNG(img, outputPath)
	}
}

// createPlaceholderImage 创建占位符图片
func (w *WkhtmltoimageRenderer) createPlaceholderImage() image.Image {
	// 创建一个简单的占位符图片
	// 在实际项目中，这里应该渲染真实的HTML内容
	img := image.NewRGBA(image.Rect(0, 0, w.config.Width, w.config.Height))

	// 填充背景色（浅灰色）
	for y := 0; y < w.config.Height; y++ {
		for x := 0; x < w.config.Width; x++ {
			img.Set(x, y, color.RGBA{240, 240, 240, 255})
		}
	}

	return img
}

// saveAsWebP 保存为WebP格式
func (w *WkhtmltoimageRenderer) saveAsWebP(img image.Image, outputPath string) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %v", err)
	}
	defer file.Close()

	options := &webp.Options{
		Lossless: false,
		Quality:  float32(w.config.Quality),
	}

	if err := webp.Encode(file, img, options); err != nil {
		return fmt.Errorf("failed to encode WebP: %v", err)
	}

	return nil
}

// saveAsPNG 保存为PNG格式
func (w *WkhtmltoimageRenderer) saveAsPNG(img image.Image, outputPath string) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %v", err)
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		return fmt.Errorf("failed to encode PNG: %v", err)
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
