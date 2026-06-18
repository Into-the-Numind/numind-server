package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/sandbox"
)

// ===========================================================================
// createDocxTool — deterministic Markdown -> .docx fast path
// ===========================================================================

// mdToDocxScript is the FIXED, version-controlled Python that converts Markdown
// to .docx. The agent never writes Python for create_docx — it only supplies the
// Markdown. We run this embedded script inside the same sandbox run_python uses.
// See scripts/md_to_docx.py for the source.
//
//go:embed scripts/md_to_docx.py
var mdToDocxScript string

// createDocxTool implements FullTool. Given Markdown content it runs the fixed
// md_to_docx.py inside a sandbox to produce a .docx, then uploads it to COS and
// returns a download URL — the fast, reliable path for STANDARD documents
// (headings / paragraphs / lists / tables / inline images). Complex custom
// layouts still go through run_python + the docx-author skill.
type createDocxTool struct {
	BaseTool
}

var _ FullTool = (*createDocxTool)(nil)

func (t *createDocxTool) Name() string { return "create_docx" }

func (t *createDocxTool) Description() string {
	return "Generate a .docx Word document from Markdown content. Use this for standard documents (headings, paragraphs, lists, tables, inline images). Faster and more reliable than writing python-docx code by hand. For complex custom layouts or precise styling, use run_python with the docx-author skill instead."
}

func (t *createDocxTool) UserFacingName() string        { return "生成 Word 文档（Markdown）" }
func (t *createDocxTool) NarrationVerb() string         { return "生成" }
func (t *createDocxTool) IsDestructive() bool           { return false }
func (t *createDocxTool) IsReadOnly() bool              { return false }
func (t *createDocxTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableSandbox }
func (t *createDocxTool) InterruptBehavior() string     { return "cancel" }
func (t *createDocxTool) MaxResultSizeChars() int       { return 4096 }

func (t *createDocxTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"markdown": {
				"type": "string",
				"description": "Markdown content for the document. Supports headings (#/##/###), paragraphs, ordered/unordered lists, tables (| a | b | with a |---| separator row), and inline images ![alt](filename) where filename refers to an image passed in input_files."
			},
			"filename": {
				"type": "string",
				"description": "Optional output filename (e.g. report.docx). The .docx extension is added automatically if missing."
			},
			"input_files": {
				"type": "array",
				"items": {"type": "string", "format": "uri"},
				"description": "Optional list of image COS URLs to embed. Reference them in markdown as ![alt](<filename>) using the file's basename."
			}
		},
		"required": ["markdown"]
	}`)
}

// ===========================================================================
// Input type
// ===========================================================================

type createDocxInput struct {
	Markdown   string   `json:"markdown"`
	Filename   string   `json:"filename,omitempty"`
	InputFiles []string `json:"input_files,omitempty"`
}

const (
	createDocxMaxInputFiles = 10
	createDocxTimeoutSecs   = 60
)

// ===========================================================================
// Execute
// ===========================================================================

func (t *createDocxTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in createDocxInput
	// Model-input and recoverable failures stay SOFT (nil Go error) so a bad
	// call does not kill the whole agent run (tool-soft-error-sweep).
	if err := json.Unmarshal(input, &in); err != nil {
		return softToolError("create_docx", "invalid input: %v", err)
	}
	if strings.TrimSpace(in.Markdown) == "" {
		return softToolError("create_docx", "'markdown' is required and must not be empty")
	}
	if len(in.InputFiles) > createDocxMaxInputFiles {
		return softToolError("create_docx", "too many input_files (%d); max is %d", len(in.InputFiles), createDocxMaxInputFiles)
	}

	// Resolve + force the .docx extension on a sanitized filename.
	filename := strings.TrimSpace(in.Filename)
	if filename == "" {
		filename = "document_" + time.Now().Format("20060102_150405") + ".docx"
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".docx") {
		filename += ".docx"
	}
	// Sandbox-safe (ASCII) name for the in-sandbox output path; the readable
	// COS key + download disposition use the original `filename`.
	sandboxName := sanitizeOutputFilename(filename)
	if !strings.HasSuffix(strings.ToLower(sandboxName), ".docx") {
		sandboxName += ".docx"
	}

	// Borrow a sandbox session (same path as run_python).
	sess := sandboxSessionForCurrentCall(ctx, "create_docx")
	if sess == nil {
		return softToolError("create_docx", "沙箱当前不可用（create_docx 依赖 sandbox），请稍后重试")
	}
	dc := dockerClientForCurrentCall(ctx)
	if dc == nil {
		return softToolError("create_docx", "沙箱客户端未初始化，请联系管理员")
	}

	if err := dc.ExecMkdir(ctx, sess.ContainerID, "/workdir/input", "/workdir/output"); err != nil {
		return softToolError("create_docx", "沙箱目录初始化失败: %v", err)
	}

	// Download + mount any input images to /workdir/input/<basename>.
	rp := &runPythonTool{}
	for _, fileURL := range in.InputFiles {
		filenameIn := extractFilenameFromURL(fileURL)
		if filenameIn == "" {
			filenameIn = "input_file"
		}
		data, err := rp.downloadInputFile(ctx, fileURL)
		if err != nil {
			return softToolError("create_docx", "下载输入文件失败 (%s): %v", fileURL, err)
		}
		containerPath := "/workdir/input/" + sanitizeOutputFilename(filenameIn)
		if err := dc.CopyToContainer(ctx, sess.ContainerID, containerPath, strings.NewReader(string(data))); err != nil {
			return softToolError("create_docx", "写入输入文件失败 (%s): %v", filenameIn, err)
		}
	}

	// Write the user's Markdown to the fixed source path.
	if err := dc.CopyToContainer(ctx, sess.ContainerID, "/workdir/input/source.md", strings.NewReader(in.Markdown)); err != nil {
		return softToolError("create_docx", "写入 Markdown 失败: %v", err)
	}

	// Write the FIXED converter script (LLM never supplies code here).
	if err := dc.CopyToContainer(ctx, sess.ContainerID, "/workdir/md_to_docx.py", strings.NewReader(mdToDocxScript)); err != nil {
		return softToolError("create_docx", "写入转换脚本失败: %v", err)
	}

	// Execute: python3 /workdir/md_to_docx.py <sandboxName>. shellQuoteSingle keeps
	// the (sanitized, ASCII) filename a single safe argv token.
	cmd := fmt.Sprintf("timeout %ds python3 /workdir/md_to_docx.py %s",
		createDocxTimeoutSecs, shellQuoteSingle(sandboxName))
	execRes, execErr := sandbox.ExecCommand(ctx, sess, cmd, dc)
	if execErr != nil {
		return softToolError("create_docx", "沙箱执行失败: %v", execErr)
	}
	if execRes.ExitCode != 0 {
		return softToolError("create_docx", "文档生成失败 (exit %d): %s",
			execRes.ExitCode, truncateString(execRes.Stderr, 1024))
	}

	// Collect the generated .docx from /workdir/output/.
	data, collectErr := t.readOutputDocx(ctx, sess, dc, sandboxName)
	if collectErr != nil {
		return softToolError("create_docx", "%v", collectErr)
	}
	if len(data) == 0 {
		return softToolError("create_docx", "脚本未产出 .docx 文件: %s", truncateString(execRes.Stderr, 512))
	}

	const docxMime = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	result, err := uploadGeneratedFile(ctx, data, docxMime, filename, "docx")
	if err != nil {
		return softToolError("create_docx", "upload failed: %v", err)
	}
	return result, nil
}

// readOutputDocx copies /workdir/output/ to a host temp dir and returns the bytes
// of the produced .docx (preferring the exact sandboxName, else the first .docx).
func (t *createDocxTool) readOutputDocx(
	ctx context.Context,
	sess *sandbox.Session,
	dc sandbox.DockerClient,
	sandboxName string,
) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "createdocx-output-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := dc.CopyFromContainer(ctx, sess.ContainerID, "/workdir/output/.", tmpDir); err != nil {
		return nil, fmt.Errorf("收集输出文件失败: %w", err)
	}

	// Prefer the exact expected filename.
	if data, err := os.ReadFile(filepath.Join(tmpDir, sandboxName)); err == nil {
		return data, nil
	}

	// Fall back to the first .docx in the output dir.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, fmt.Errorf("read output dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".docx") {
			return os.ReadFile(filepath.Join(tmpDir, e.Name()))
		}
	}
	return nil, fmt.Errorf("沙箱输出目录无 .docx 文件")
}

// shellQuoteSingle wraps s in single quotes for safe use as one shell argv token,
// escaping any embedded single quotes. The input here is already ASCII-sanitized
// via sanitizeOutputFilename, so this is defense-in-depth.
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
