package attachment

import (
	"fmt"
	"strings"
)

// imageTemplateData holds the inputs for composeImageFallback.
type imageTemplateData struct {
	Filename          string
	Width             *int
	Height            *int
	FilesizeKB        int64
	VisionDescription string // may be empty
	OCRText           string // may be empty
}

// ImageTemplateDataExported is the exported alias of imageTemplateData for use
// in external test packages (package attachment_test).
type ImageTemplateDataExported = imageTemplateData

// ComposeImageFallbackExported is the exported wrapper around composeImageFallback
// for use in external test packages.
func ComposeImageFallbackExported(d imageTemplateData) string {
	return composeImageFallback(d)
}

// composeImageFallback builds the text_fallback string for an image attachment.
// The template respects spec §3:
//   - Both VLM and OCR present → include both sections
//   - Only OCR → skip VLM section
//   - Only VLM → skip OCR section
//   - Neither → degenerate "[图片：{filename}，文字描述不可用]"
func composeImageFallback(d imageTemplateData) string {
	var dimStr string
	if d.Width != nil && d.Height != nil {
		dimStr = fmt.Sprintf("%dx%d，", *d.Width, *d.Height)
	}

	header := fmt.Sprintf("[图片：%s（%s%dKB）\n当前模型不支持直接看图，以下是该图的文字描述：",
		d.Filename, dimStr, d.FilesizeKB)

	var sections []string

	if d.VisionDescription != "" {
		sections = append(sections, fmt.Sprintf("\n画面描述：\n%s", d.VisionDescription))
	}

	if d.OCRText != "" {
		sections = append(sections, fmt.Sprintf("\nOCR提取的文字：\n%s", d.OCRText))
	}

	if len(sections) == 0 {
		return fmt.Sprintf("[图片：%s，文字描述不可用]", d.Filename)
	}

	return header + strings.Join(sections, "") + "\n]"
}

// composePDFFallback builds the text_fallback string for a PDF attachment.
func composePDFFallback(filename string, filesizeKB int64, extractedText string) string {
	if extractedText == "" {
		return fmt.Sprintf("[PDF：%s（%dKB），文本提取失败]", filename, filesizeKB)
	}
	return fmt.Sprintf("[PDF：%s（%dKB）\n全文文本提取：\n%s\n]",
		filename, filesizeKB, extractedText)
}

// composeDocumentFallback builds the text_fallback string for an office document
// attachment (docx/doc/pptx/xlsx/rtf) whose text was extracted locally.
func composeDocumentFallback(filename string, filesizeKB int64, extractedText string) string {
	if extractedText == "" {
		return fmt.Sprintf("[文档：%s（%dKB），文本提取失败]", filename, filesizeKB)
	}
	return fmt.Sprintf("[文档：%s（%dKB）\n全文文本提取：\n%s\n]",
		filename, filesizeKB, extractedText)
}

// composeAudioFallback builds the text_fallback string for an audio attachment.
func composeAudioFallback(filename string, durationSec float64, transcript string) string {
	if transcript == "" {
		return fmt.Sprintf("[音频：%s，语音转文字失败]", filename)
	}
	return fmt.Sprintf("[音频：%s（%.0fs）\n语音转文字：\n%s\n]",
		filename, durationSec, transcript)
}

// composeErrorFallback returns the degraded message when all retries failed.
// text_fallback is still set so that buildAgentInput (task 1.3) has something
// to inject — the user sees an informative placeholder rather than a blank.
func composeErrorFallback(filename, modality, errMsg string) string {
	switch modality {
	case ModalityImage:
		return fmt.Sprintf("[图片：%s，描述生成失败：%s]", filename, errMsg)
	case ModalityPDF:
		return fmt.Sprintf("[PDF：%s，文本提取失败：%s]", filename, errMsg)
	case ModalityAudio:
		return fmt.Sprintf("[音频：%s，语音转文字失败：%s]", filename, errMsg)
	case ModalityDocument:
		return fmt.Sprintf("[文档：%s，文本提取失败：%s]", filename, errMsg)
	default:
		return fmt.Sprintf("[文件：%s，处理失败：%s]", filename, errMsg)
	}
}

// ComposePendingFallback is inserted by buildAgentInput (task 1.3) when
// WaitReady times out — the file is still being processed.
// Exported so that task 1.3 (biz/agent/input.go) can call it without
// duplicating the template string.
func ComposePendingFallback(filename string) string {
	return fmt.Sprintf("[图片：%s，描述正在生成中，请稍后重试或切换到多模态模型]", filename)
}
