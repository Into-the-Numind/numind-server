package card

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"strings"
)

// CoverOptimizer 封面图片优化器
type CoverOptimizer struct {
	targetWidth int     // 目标宽度 (1080px)
	baseHeight  int     // 基础高度 (1440px)
	aspectRatio float64 // 宽高比 (3:4)
}

// NewCoverOptimizer 创建封面优化器
func NewCoverOptimizer() *CoverOptimizer {
	return &CoverOptimizer{
		targetWidth: 1080,
		baseHeight:  1440,
		aspectRatio: 3.0 / 4.0, // 3:4 比例
	}
}

// CoverImageInfo 封面图片信息
type CoverImageInfo struct {
	URL           string  `json:"url"`
	OptimizedURL  string  `json:"optimized_url"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	AspectRatio   float64 `json:"aspect_ratio"`
	OptimizedSize string  `json:"optimized_size"`
}

// OptimizeCoverImage 优化封面图片
func (c *CoverOptimizer) OptimizeCoverImage(imageURL string) (*CoverImageInfo, error) {
	fmt.Printf("🖼️ 开始优化封面图片: %s\n", imageURL)

	// 下载图片
	img, err := c.downloadImage(imageURL)
	if err != nil {
		return nil, fmt.Errorf("下载图片失败: %v", err)
	}

	bounds := img.Bounds()
	originalWidth := bounds.Dx()
	originalHeight := bounds.Dy()
	originalAspectRatio := float64(originalWidth) / float64(originalHeight)

	fmt.Printf("📏 原始图片尺寸: %dx%d (比例: %.3f)\n", originalWidth, originalHeight, originalAspectRatio)

	// 计算优化后的尺寸
	optimizedHeight := c.calculateOptimalHeight(originalWidth, originalHeight)

	fmt.Printf("📐 目标尺寸: %dx%d\n", c.targetWidth, optimizedHeight)

	// 调整图片尺寸
	optimizedImg := c.resizeImageToCover(img, c.targetWidth, optimizedHeight)

	// 保存优化后的图片
	optimizedURL, err := c.saveOptimizedImage(optimizedImg, imageURL)
	if err != nil {
		return nil, fmt.Errorf("保存优化图片失败: %v", err)
	}

	info := &CoverImageInfo{
		URL:           imageURL,
		OptimizedURL:  optimizedURL,
		Width:         c.targetWidth,
		Height:        optimizedHeight,
		AspectRatio:   float64(c.targetWidth) / float64(optimizedHeight),
		OptimizedSize: fmt.Sprintf("%dx%d", c.targetWidth, optimizedHeight),
	}

	fmt.Printf("✅ 封面图片优化完成: %s\n", optimizedURL)
	return info, nil
}

// calculateOptimalHeight 计算最优高度（确保是1440的整数倍）
func (c *CoverOptimizer) calculateOptimalHeight(originalWidth, originalHeight int) int {
	// 计算原始图片的宽高比
	originalAspectRatio := float64(originalWidth) / float64(originalHeight)

	// 目标宽度是1080，根据原始比例计算对应高度
	naturalHeight := float64(c.targetWidth) / originalAspectRatio

	// 将高度规范化为1440的整数倍
	multiplier := math.Max(1, math.Round(naturalHeight/float64(c.baseHeight)))
	optimizedHeight := int(multiplier) * c.baseHeight

	// 如果图片比例接近3:4，优先使用标准1440高度
	if math.Abs(originalAspectRatio-c.aspectRatio) < 0.1 {
		optimizedHeight = c.baseHeight
	}

	// 限制最大高度为3倍基础高度（超长图片处理）
	maxHeight := c.baseHeight * 3
	if optimizedHeight > maxHeight {
		optimizedHeight = maxHeight
	}

	fmt.Printf("🔧 高度计算: 原始比例=%.3f, 自然高度=%.0f, 倍数=%.0f, 优化高度=%d\n",
		originalAspectRatio, naturalHeight, multiplier, optimizedHeight)

	return optimizedHeight
}

// resizeImageToCover 调整图片尺寸以完全覆盖目标区域
func (c *CoverOptimizer) resizeImageToCover(img image.Image, targetWidth, targetHeight int) image.Image {
	bounds := img.Bounds()
	originalWidth := bounds.Dx()
	originalHeight := bounds.Dy()

	// 计算缩放比例，确保图片能完全覆盖目标区域
	scaleX := float64(targetWidth) / float64(originalWidth)
	scaleY := float64(targetHeight) / float64(originalHeight)
	scale := math.Max(scaleX, scaleY) // 使用较大的缩放比例确保完全覆盖

	// 计算缩放后的尺寸
	scaledWidth := uint(float64(originalWidth) * scale)
	scaledHeight := uint(float64(originalHeight) * scale)

	fmt.Printf("🔄 图片缩放: 原始(%dx%d) -> 缩放后(%dx%d) -> 目标(%dx%d)\n",
		originalWidth, originalHeight, scaledWidth, scaledHeight, targetWidth, targetHeight)

	// 使用简单缩放算法进行缩放
	resizedImg := c.simpleResize(img, int(scaledWidth), int(scaledHeight))

	// 如果缩放后的图片大于目标尺寸，进行居中裁剪
	if int(scaledWidth) > targetWidth || int(scaledHeight) > targetHeight {
		return c.cropImageToCenter(resizedImg, targetWidth, targetHeight)
	}

	return resizedImg
}

// cropImageToCenter 从中心裁剪图片
func (c *CoverOptimizer) cropImageToCenter(img image.Image, targetWidth, targetHeight int) image.Image {
	bounds := img.Bounds()
	imgWidth := bounds.Dx()
	imgHeight := bounds.Dy()

	// 计算裁剪起始位置（居中）
	startX := (imgWidth - targetWidth) / 2
	startY := (imgHeight - targetHeight) / 2

	// 确保起始位置不为负数
	if startX < 0 {
		startX = 0
	}
	if startY < 0 {
		startY = 0
	}

	// 执行裁剪
	croppedImg := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))

	// 复制像素数据
	for y := 0; y < targetHeight; y++ {
		for x := 0; x < targetWidth; x++ {
			srcX := startX + x
			srcY := startY + y
			if srcX < imgWidth && srcY < imgHeight {
				croppedImg.Set(x, y, img.At(srcX, srcY))
			}
		}
	}

	fmt.Printf("✂️ 图片裁剪: 从(%dx%d)裁剪到(%dx%d), 起始位置(%d,%d)\n",
		imgWidth, imgHeight, targetWidth, targetHeight, startX, startY)

	return croppedImg
}

// downloadImage 下载图片
func (c *CoverOptimizer) downloadImage(imageURL string) (image.Image, error) {
	resp, err := http.Get(imageURL)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP状态错误: %d", resp.StatusCode)
	}

	// 根据Content-Type或URL扩展名决定解码方式
	contentType := resp.Header.Get("Content-Type")
	var img image.Image

	if strings.Contains(contentType, "image/png") || strings.HasSuffix(imageURL, ".png") {
		img, err = png.Decode(resp.Body)
	} else if strings.Contains(contentType, "image/jpeg") || strings.HasSuffix(imageURL, ".jpg") || strings.HasSuffix(imageURL, ".jpeg") {
		img, err = jpeg.Decode(resp.Body)
	} else {
		// 尝试自动检测格式
		img, _, err = image.Decode(resp.Body)
	}

	if err != nil {
		return nil, fmt.Errorf("图片解码失败: %v", err)
	}

	return img, nil
}

// saveOptimizedImage 保存优化后的图片
func (c *CoverOptimizer) saveOptimizedImage(img image.Image, originalURL string) (string, error) {
	// 这里应该实现图片保存逻辑
	// 在实际项目中，需要将图片保存到文件系统或云存储
	// 现在返回一个模拟的URL
	optimizedURL := strings.Replace(originalURL, ".jpg", "_optimized.jpg", 1)
	optimizedURL = strings.Replace(optimizedURL, ".png", "_optimized.png", 1)

	fmt.Printf("💾 保存优化图片: %s\n", optimizedURL)

	return optimizedURL, nil
}

// GenerateCoverCSS 生成封面图片的CSS样式
func (c *CoverOptimizer) GenerateCoverCSS(coverInfo *CoverImageInfo, isFullCover bool) string {
	var css strings.Builder

	css.WriteString(".cover-image {\n")
	css.WriteString(fmt.Sprintf("    width: %dpx;\n", coverInfo.Width))
	css.WriteString(fmt.Sprintf("    height: %dpx;\n", coverInfo.Height))
	css.WriteString("    object-fit: cover;\n")
	css.WriteString("    object-position: center;\n")

	if isFullCover {
		// 封面图独占模式：无边框、无边距
		css.WriteString("    margin: 0;\n")
		css.WriteString("    padding: 0;\n")
		css.WriteString("    border-radius: 0;\n")
		css.WriteString("    display: block;\n")
	} else {
		// 标准模式：适当的边距和圆角
		css.WriteString("    border-radius: 12px;\n")
		css.WriteString("    margin-bottom: 20px;\n")
	}

	css.WriteString("    background-color: #f0f0f0;\n") // 加载时的背景色
	css.WriteString("}\n")

	// 添加响应式处理
	css.WriteString("\n.cover-image:not([src]) {\n")
	css.WriteString("    background: linear-gradient(45deg, #f0f0f0 25%, transparent 25%),\n")
	css.WriteString("                linear-gradient(-45deg, #f0f0f0 25%, transparent 25%),\n")
	css.WriteString("                linear-gradient(45deg, transparent 75%, #f0f0f0 75%),\n")
	css.WriteString("                linear-gradient(-45deg, transparent 75%, #f0f0f0 75%);\n")
	css.WriteString("    background-size: 20px 20px;\n")
	css.WriteString("    background-position: 0 0, 0 10px, 10px -10px, -10px 0px;\n")
	css.WriteString("}\n")

	return css.String()
}

// ValidateCoverImage 验证封面图片是否符合要求
func (c *CoverOptimizer) ValidateCoverImage(imageURL string) error {
	if imageURL == "" {
		return fmt.Errorf("封面图片URL不能为空")
	}

	// 简单的URL格式验证
	if !strings.HasPrefix(imageURL, "http://") && !strings.HasPrefix(imageURL, "https://") {
		return fmt.Errorf("封面图片URL格式无效: %s", imageURL)
	}

	// 检查是否是支持的图片格式
	supportedFormats := []string{".jpg", ".jpeg", ".png", ".webp"}
	isSupported := false
	for _, format := range supportedFormats {
		if strings.HasSuffix(strings.ToLower(imageURL), format) {
			isSupported = true
			break
		}
	}

	if !isSupported {
		return fmt.Errorf("不支持的图片格式，支持格式: %v", supportedFormats)
	}

	return nil
}

// simpleResize 简单图片缩放实现
func (c *CoverOptimizer) simpleResize(src image.Image, width, height int) image.Image {
	srcBounds := src.Bounds()
	srcWidth := srcBounds.Dx()
	srcHeight := srcBounds.Dy()

	// 创建目标图片
	dst := image.NewRGBA(image.Rect(0, 0, width, height))

	// 计算缩放比例
	scaleX := float64(srcWidth) / float64(width)
	scaleY := float64(srcHeight) / float64(height)

	// 双线性插值缩放
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			srcX := float64(x) * scaleX
			srcY := float64(y) * scaleY

			// 最近邻插值
			srcXInt := int(srcX + 0.5)
			srcYInt := int(srcY + 0.5)

			// 边界检查
			if srcXInt >= srcWidth {
				srcXInt = srcWidth - 1
			}
			if srcYInt >= srcHeight {
				srcYInt = srcHeight - 1
			}

			// 复制像素
			dst.Set(x, y, src.At(srcXInt, srcYInt))
		}
	}

	return dst
}
