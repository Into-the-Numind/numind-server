package agent

import (
	"context"
	"encoding/json"
	"strings"
)

// createDocxTool implements the native standard Markdown -> .docx path.
// Complex custom layout still belongs on load_skill("docx-author") -> run_python.
type createDocxTool struct {
	BaseTool
}

var _ FullTool = (*createDocxTool)(nil)

func (t *createDocxTool) Name() string { return "create_docx" }

func (t *createDocxTool) Description() string {
	return "Generate a standard .docx Word document from Markdown content without using the sandbox. Use for headings, paragraphs, lists, simple tables, and basic inline images. For complex custom layouts, exact styling, templates, or advanced image placement, use load_skill(\"docx-author\") then run_python."
}

func (t *createDocxTool) UserFacingName() string      { return "生成 Word 文档（Markdown）" }
func (t *createDocxTool) NarrationVerb() string       { return "生成" }
func (t *createDocxTool) IsDestructive() bool         { return false }
func (t *createDocxTool) IsReadOnly() bool            { return false }
func (t *createDocxTool) IsEnabled(_ ToolConfig) bool { return true }
func (t *createDocxTool) InterruptBehavior() string   { return "cancel" }
func (t *createDocxTool) MaxResultSizeChars() int     { return 4096 }

func (t *createDocxTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"markdown": {
				"type": "string",
				"description": "Markdown content for the document. Supports headings (#/##/###), paragraphs, ordered/unordered lists, simple tables, and standalone image lines ![alt](filename) where filename refers to an image passed in input_files."
			},
			"filename": {
				"type": "string",
				"description": "Optional output filename (e.g. report.docx). The .docx extension is added automatically if missing."
			},
			"input_files": {
				"type": "array",
				"items": {"type": "string", "format": "uri"},
				"description": "Optional list of image COS URLs to embed. Reference them in markdown as standalone image lines using the file basename."
			}
		},
		"required": ["markdown"]
	}`)
}

type createDocxInput struct {
	Markdown   string   `json:"markdown"`
	Filename   string   `json:"filename,omitempty"`
	InputFiles []string `json:"input_files,omitempty"`
}

const createDocxMaxInputFiles = 10

func (t *createDocxTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createDocxInput
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("create_docx", "invalid input: %v", err)
	}
	if strings.TrimSpace(in.Markdown) == "" {
		return softToolError("create_docx", "'markdown' is required and must not be empty")
	}
	if len(in.InputFiles) > createDocxMaxInputFiles {
		return softToolError("create_docx", "too many input_files (%d); max is %d", len(in.InputFiles), createDocxMaxInputFiles)
	}

	images, err := t.loadNativeImages(ctx, in.InputFiles)
	if err != nil {
		return softToolError("create_docx", "%v", err)
	}
	data, err := buildNativeDocx(nativeDocxInput{Markdown: in.Markdown, Images: images})
	if err != nil {
		return softToolError("create_docx", "generate docx: %v", err)
	}

	filename := resolveOfficeFilename(in.Filename, "document", ".docx")
	result, err := uploadGeneratedFile(ctx, data, docxContentTypeNative, filename, "docx")
	if err != nil {
		return softToolError("create_docx", "upload failed: %v", err)
	}
	return result, nil
}

func (t *createDocxTool) loadNativeImages(ctx context.Context, inputFiles []string) (map[string]nativeDocxImage, error) {
	if len(inputFiles) == 0 {
		return nil, nil
	}
	rp := &runPythonTool{}
	images := make(map[string]nativeDocxImage, len(inputFiles)*2)
	for _, fileURL := range inputFiles {
		filename := extractFilenameFromURL(fileURL)
		data, err := rp.downloadInputFile(ctx, fileURL)
		if err != nil {
			return nil, err
		}
		img, err := decodeNativeDocxImage(filename, data)
		if err != nil {
			return nil, err
		}
		for _, key := range []string{filename, filepathBase(filename), img.Name} {
			images[strings.ToLower(key)] = img
		}
	}
	return images, nil
}

func filepathBase(path string) string {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
