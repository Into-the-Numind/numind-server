package image

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/google/uuid"
)

// AsyncImageProcessor 异步图片处理器
type AsyncImageProcessor struct {
	biz      BizInterface
	baiduBiz BaiduBiz
	aliBiz   AliBiz
	volcBiz  VolcBiz // 新增volc支持
}

// BizInterface 业务接口
type BizInterface interface {
	Images() AsyncImageBiz
	Books() AsyncBookBiz
}

// AsyncImageBiz 图片业务接口
type AsyncImageBiz interface {
	Create(ctx context.Context, image *model.ImageM) error
}

// AsyncBookBiz 书籍业务接口
type AsyncBookBiz interface {
	Create(ctx context.Context, book *model.BookM) error
}

// BaiduBiz 百度业务接口
type BaiduBiz interface {
	OCRImage(imageData []byte) (string, error)
}

// AliBiz 阿里业务接口
type AliBiz interface {
	QianwenTextStream(messages []map[string]string, maxTokens int, temperature float64) (string, error)
	WanxiangImageAsync(prompt, style, size string) (string, error)
	StableDiffusionImageAsync(prompt, size string) (string, error)
}

// VolcBiz 火山引擎业务接口
type VolcBiz interface {
	VolcTextStream(ctx context.Context, messages []map[string]string, maxTokens int, temperature float64) (string, error)
}

// NewAsyncImageProcessor 创建异步图片处理器
func NewAsyncImageProcessor(biz BizInterface, baiduBiz BaiduBiz, aliBiz AliBiz, volcBiz VolcBiz) *AsyncImageProcessor {
	return &AsyncImageProcessor{
		biz:      biz,
		baiduBiz: baiduBiz,
		aliBiz:   aliBiz,
		volcBiz:  volcBiz,
	}
}

// ProcessImagesAsync 异步处理图片
func (p *AsyncImageProcessor) ProcessImagesAsync(ctx context.Context, userID uint, files []*multipart.FileHeader) (string, error) {
	// 生成任务ID
	taskID := uuid.New().String()

	// 立即返回任务ID
	go func() {
		p.processImagesInBackground(ctx, taskID, userID, files)
	}()

	return taskID, nil
}

// processImagesInBackground 在后台处理图片
func (p *AsyncImageProcessor) processImagesInBackground(ctx context.Context, taskID string, userID uint, files []*multipart.FileHeader) {
	startTime := time.Now()

	// 确保目录存在
	_ = os.MkdirAll("uploads", os.ModePerm)

	// 定义处理结果结构
	type ProcessResult struct {
		Filename      string
		URL           string
		CombinedText  string
		QianwenResult string
		Error         error
	}

	// 使用channel收集结果
	resultChan := make(chan ProcessResult, len(files))

	// 使用信号量控制最多2个并发OCR
	ocrSem := make(chan struct{}, 2)

	var wg sync.WaitGroup

	for _, fileHeader := range files {
		wg.Add(1)
		ocrSem <- struct{}{} // 占用一个槽位

		go func(fh *multipart.FileHeader) {
			defer func() {
				<-ocrSem // 释放槽位
				wg.Done()
			}()

			result := ProcessResult{
				Filename: fmt.Sprintf("%d_%s", time.Now().UnixNano(), fh.Filename),
			}

			// 打开文件并读取数据
			file, err := fh.Open()
			if err != nil {
				result.Error = fmt.Errorf("open file failed: %w", err)
				resultChan <- result
				return
			}

			data, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				result.Error = fmt.Errorf("read file failed: %w", err)
				resultChan <- result
				return
			}

			// 保存文件到本地
			dst := filepath.Join("uploads", result.Filename)
			if err := saveUploadedFile(fh, dst); err != nil {
				result.Error = fmt.Errorf("save file failed: %w", err)
				resultChan <- result
				return
			}
			result.URL = dst

			// 调用百度OCR识别图片文字
			ocrText, err := p.baiduBiz.OCRImage(data)
			if err != nil {
				result.Error = fmt.Errorf("OCR failed: %w", err)
				resultChan <- result
				return
			}

			// 清理OCR文本
			cleanedText := cleanOCRText(ocrText)
			if cleanedText == "" {
				result.Error = fmt.Errorf("OCR result is empty")
				resultChan <- result
				return
			}

			result.CombinedText = cleanedText

			// 调用火山引擎文字模型处理文本（替换原来的千问）
			prompt := `# 角色
你是一位顶级的内容策略师和信息设计师。你深刻理解"形式服务于内容"的原则。你的专长是将原始、无序的碎片化信息，转化为兼具逻辑深度与视觉美感的最终作品。

# 任务
请处理我提供的文本片段，将其整理成结构化的、易于阅读的格式。

# 要求
1. **直接输出：** 你的回复必须直接以最终成品的标题开始。**严禁**包含任何"好的，这是为您整理的结果："之类的开场白或解释性文字。
2. **内容完整性：** 你必须输出所有被处理过的内容。任何一个被判断为有效的片段，都必须出现在最终的成品中（无论是在主体部分还是在"其他零散记录"部分）。
3. **格式纯粹性：** 最终输出只允许包含标题、小标题、自然段落、以及在特定规则下允许的项目/编号列表。

# 开始工作
现在，请确认你已完全理解以上所有规则。然后，处理我接下来提供给你的文本片段。

[片段1] ` + result.CombinedText

			messages := []map[string]string{
				{"role": "user", "content": prompt},
			}

			// 首先尝试调用火山方舟
			volcResult, err := p.volcBiz.VolcTextStream(ctx, messages, 1024, 0.5)
			if err != nil {
				log.C(ctx).Warnw("⚠️ 火山方舟API失败，尝试阿里百炼降级", "filename", result.Filename, "error", err.Error())

				// 降级到阿里百炼
				qianwenResult, err := p.aliBiz.QianwenTextStream(messages, 1024, 0.5)
				if err != nil {
					log.C(ctx).Errorw("❌ 所有AI API都失败", "filename", result.Filename, "volc_error", err.Error(), "qianwen_error", err.Error())
				} else {
					log.C(ctx).Infow("✅ 阿里百炼API降级成功", "filename", result.Filename, "result", qianwenResult)
					volcResult = qianwenResult
				}
			} else {
				log.C(ctx).Infow("✅ 火山方舟API调用成功", "filename", result.Filename, "result", volcResult)
			}

			if volcResult != "" {
				result.QianwenResult = volcResult // 保持字段名不变，避免影响其他代码
			}

			resultChan <- result
		}(fileHeader)
	}

	// 等待所有goroutine完成
	wg.Wait()
	close(resultChan)

	// 收集所有结果
	var processedImages []ProcessedImage
	var allCombinedTexts []string
	var hasError bool

	for result := range resultChan {
		if result.Error != nil {
			log.C(ctx).Errorw("File processing failed", "filename", result.Filename, "error", result.Error.Error())
			hasError = true
			continue
		}

		allCombinedTexts = append(allCombinedTexts, result.CombinedText)

		processedImage := ProcessedImage{
			Filename:      result.Filename,
			URL:           result.URL,
			OriginalText:  result.CombinedText,
			QianwenResult: result.QianwenResult,
		}
		processedImages = append(processedImages, processedImage)
	}

	// 如果所有文件都处理失败
	if hasError && len(processedImages) == 0 {
		log.C(ctx).Errorw("All images processing failed")
		return
	}

	// 所有图片处理完成后，整合所有文本并创建书籍记录
	if len(allCombinedTexts) > 0 {
		// 将所有文本整合成一个完整的文本
		finalCombinedText := strings.Join(allCombinedTexts, "\n\n")
		log.C(ctx).Infow("Final combined text for book creation", "text", finalCombinedText)

		// 跳过图片生成，直接创建书籍记录
		log.C(ctx).Infow("⚠️ 跳过图片生成步骤，直接创建书籍记录", "task_id", taskID, "user_id", userID)

		// 创建书籍记录
		bookRecord := &model.BookM{
			UserID:    userID,
			Title:     "AI生成的书籍",
			CardCount: len(allCombinedTexts), // 使用处理的图片数量作为卡片数量
		}

		if err := p.biz.Books().Create(ctx, bookRecord); err != nil {
			log.C(ctx).Errorw("Failed to create book record", "error", err.Error())
		} else {
			log.C(ctx).Infow("Book created successfully", "book_id", bookRecord.ID, "user_id", userID)
		}
	}

	// 记录处理完成
	processingTime := time.Since(startTime)
	log.C(ctx).Infow("Image processing completed",
		"task_id", taskID,
		"user_id", userID,
		"processed_count", len(processedImages),
		"processing_time", processingTime)
}

// ProcessedImage 处理后的图片信息
type ProcessedImage struct {
	Filename      string `json:"filename"`
	URL           string `json:"url"`
	OriginalText  string `json:"original_text"`
	QianwenResult string `json:"qianwen_result"`
}

// FinalProcessingResult 最终处理结果
type FinalProcessingResult struct {
	WanxiangResult string `json:"wanxiang_result"`
	BookID         uint   `json:"book_id,omitempty"`
	TotalTexts     int    `json:"total_texts"`
}

// ImageProcessingResult 图片处理结果
type ImageProcessingResult struct {
	TaskID          string                 `json:"task_id"`
	UserID          uint                   `json:"user_id"`
	Status          string                 `json:"status"`
	ProcessedImages []ProcessedImage       `json:"processed_images"`
	FinalResult     *FinalProcessingResult `json:"final_result,omitempty"`
	ErrorMessage    string                 `json:"error_message,omitempty"`
	ProcessingTime  time.Duration          `json:"processing_time"`
	CreatedAt       time.Time              `json:"created_at"`
}

// saveUploadedFile 保存上传的文件
func saveUploadedFile(fileHeader *multipart.FileHeader, dst string) error {
	src, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer src.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, src)
	return err
}

// cleanOCRText 清理OCR文本
func cleanOCRText(text string) string {
	// 移除多余的空白字符
	text = strings.TrimSpace(text)

	// 移除空行
	lines := strings.Split(text, "\n")
	var cleanedLines []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			cleanedLines = append(cleanedLines, strings.TrimSpace(line))
		}
	}

	return strings.Join(cleanedLines, "\n")
}
