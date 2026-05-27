package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/pkg/util"
)

// ===========================================================================
// runPythonTool — Layer 3 last-resort file generation via sandboxed Python
// ===========================================================================

// runPythonTool implements FullTool. It executes arbitrary Python 3 code
// inside an isolated Docker sandbox and uploads any files written to /output/
// to COS, returning their presigned URLs.
//
// Use ONLY when Layer 1 (create_csv/html/json/text/png_chart) and Layer 2
// (invoke_skill: xlsx/docx/pptx/pdf) do not cover the required output format.
type runPythonTool struct {
	BaseTool
}

var _ FullTool = (*runPythonTool)(nil)

func (t *runPythonTool) Name() string { return "run_python" }

func (t *runPythonTool) Description() string {
	return `Execute arbitrary Python 3 code inside an isolated sandbox to generate files in long-tail formats not covered by Layer 1 or Layer 2 tools.

⚠️  LAST RESORT ONLY — use this tool ONLY when ALL of the following conditions are true:
  1. The required output format is NOT supported by create_csv, create_html, create_json, or create_text (Layer 1 tools).
  2. The required output format is NOT covered by the skill tools: create_excel_xlsx, create_word_docx, create_ppt_pptx, create_pdf, or create_png_chart.
  3. The format is genuinely unusual (e.g. .ical, .vcf, .yaml, .xml, Mermaid diagram rendering, .gpx, .midi, etc.).

Do NOT use run_python when:
  - You need CSV → use create_csv instead.
  - You need HTML → use create_html instead.
  - You need JSON → use create_json instead.
  - You need plain text → use create_text instead.
  - You need PNG chart → use create_png_chart instead.
  - You need Excel/XLSX → use create_excel_xlsx skill instead.
  - You need Word/DOCX → use create_word_docx skill instead.
  - You need PowerPoint/PPTX → use create_ppt_pptx skill instead.
  - You need PDF → use create_pdf skill instead.
  - You just want to run arbitrary Python logic without file output → this is the wrong tool; reconsider your approach.

Input files (COS URLs) are mounted read-only at /workspace/input/<filename>. Output files must be written to /output/. Execution timeout: 30s default (max 120s). Resource limits: 256MB RAM, 1 CPU. Returns list of COS URLs for each generated output file.`
}

func (t *runPythonTool) UserFacingName() string        { return "Python 代码执行（文件生成）" }
func (t *runPythonTool) NarrationVerb() string         { return "执行" }
func (t *runPythonTool) IsDestructive() bool           { return true }
func (t *runPythonTool) IsReadOnly() bool              { return false }
func (t *runPythonTool) IsEnabled(cfg ToolConfig) bool { return cfg.EnableSandbox }
func (t *runPythonTool) InterruptBehavior() string     { return "cancel" }
func (t *runPythonTool) MaxResultSizeChars() int       { return 16384 }

func (t *runPythonTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"code": {
				"type": "string",
				"description": "Python 3 code to execute. Write output files to /output/ directory. Example: open('/output/result.ical','w').write(...)."
			},
			"input_files": {
				"type": "array",
				"items": {"type": "string", "format": "uri"},
				"description": "Optional list of COS URLs to download as inputs. Available as /workspace/input/<filename> inside the sandbox."
			},
			"expected_output_files": {
				"type": "array",
				"items": {"type": "string"},
				"description": "Expected output filenames under /output/. If empty, all files in /output/ are collected."
			},
			"timeout_seconds": {
				"type": "integer",
				"minimum": 1,
				"maximum": 120,
				"default": 30,
				"description": "Execution timeout in seconds (max 120)."
			}
		},
		"required": ["code"]
	}`)
}

// ===========================================================================
// Input / Output types
// ===========================================================================

type runPythonInput struct {
	// Python 3 code to execute. Write output files to /output/.
	Code string `json:"code"`

	// Optional COS URLs to download into /workspace/input/<filename>.
	InputFiles []string `json:"input_files,omitempty"`

	// Expected filenames under /output/. If empty, collect all files.
	ExpectedOutputFiles []string `json:"expected_output_files,omitempty"`

	// Timeout in seconds; 0 = default 30s; max 120s.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

type runPythonOutput struct {
	Files      []runPythonFileResult `json:"files"`
	Stdout     string                `json:"stdout,omitempty"`
	Stderr     string                `json:"stderr,omitempty"`
	ExitCode   int                   `json:"exit_code"`
	DurationMs int64                 `json:"duration_ms"`
}

type runPythonFileResult struct {
	Filename  string `json:"filename"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
}

// ===========================================================================
// Constants
// ===========================================================================

const (
	runPythonDefaultTimeoutSecs = 30
	runPythonMaxTimeoutSecs     = 120
	runPythonMaxInputFiles      = 5
	runPythonMaxOutputFiles     = 20
	runPythonStdoutMaxBytes     = 4096
	runPythonStderrMaxBytes     = 2048
)

// ===========================================================================
// Execute
// ===========================================================================

func (t *runPythonTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	var in runPythonInput
	if err := json.Unmarshal(input, &in); err != nil {
		return runPythonFriendlyError("run_python: invalid JSON input: " + err.Error()), nil
	}

	// Validate
	if strings.TrimSpace(in.Code) == "" {
		return runPythonFriendlyError("run_python: 'code' field is required and must not be empty"), nil
	}
	if len(in.InputFiles) > runPythonMaxInputFiles {
		return runPythonFriendlyError(fmt.Sprintf("run_python: too many input_files (%d); max is %d", len(in.InputFiles), runPythonMaxInputFiles)), nil
	}
	if len(in.ExpectedOutputFiles) > runPythonMaxOutputFiles {
		return runPythonFriendlyError(fmt.Sprintf("run_python: too many expected_output_files (%d); max is %d", len(in.ExpectedOutputFiles), runPythonMaxOutputFiles)), nil
	}

	// Clamp timeout
	timeoutSecs := in.TimeoutSeconds
	if timeoutSecs <= 0 {
		timeoutSecs = runPythonDefaultTimeoutSecs
	}
	if timeoutSecs > runPythonMaxTimeoutSecs {
		timeoutSecs = runPythonMaxTimeoutSecs
	}

	// Borrow sandbox session
	sess := sandboxSessionForCurrentCall(ctx, "run_python")
	if sess == nil {
		return runPythonFriendlyError("沙箱当前不可用（run_python 依赖 sandbox），请稍后重试"), nil
	}
	dc := dockerClientForCurrentCall(ctx)
	if dc == nil {
		return runPythonFriendlyError("沙箱客户端未初始化，请联系管理员"), nil
	}

	start := time.Now()

	// Step 1: Create directory structure inside sandbox
	if err := dc.ExecMkdir(ctx, sess.ContainerID, "/workspace/input", "/output"); err != nil {
		return runPythonFriendlyError(fmt.Sprintf("run_python: 沙箱目录初始化失败: %v", err)), nil
	}

	// Step 2: Download and mount input files
	for _, fileURL := range in.InputFiles {
		filename := extractFilenameFromURL(fileURL)
		if filename == "" {
			filename = "input_file"
		}
		data, err := t.downloadInputFile(ctx, fileURL)
		if err != nil {
			return runPythonFriendlyError(fmt.Sprintf("run_python: 下载输入文件失败 (%s): %v", fileURL, err)), nil
		}
		containerPath := "/workspace/input/" + sanitizeOutputFilename(filename)
		if err := t.writeFileToSandbox(ctx, sess, containerPath, data, dc); err != nil {
			return runPythonFriendlyError(fmt.Sprintf("run_python: 写入输入文件失败 (%s): %v", filename, err)), nil
		}
	}

	// Step 3: Write Python code to /workdir/run.py
	if err := t.writeFileToSandbox(ctx, sess, "/workdir/run.py", []byte(in.Code), dc); err != nil {
		return runPythonFriendlyError(fmt.Sprintf("run_python: 写入 Python 代码失败: %v", err)), nil
	}

	// Step 4: Execute the Python script
	cmd := fmt.Sprintf("timeout %ds python3 /workdir/run.py", timeoutSecs)
	execRes, execErr := sandbox.ExecCommand(ctx, sess, cmd, dc)
	if execErr != nil {
		return runPythonFriendlyError(fmt.Sprintf("run_python: 沙箱执行失败: %v", execErr)), nil
	}

	durationMs := time.Since(start).Milliseconds()

	// Step 5: Collect output files
	files, collectErr := t.collectOutputFiles(ctx, sess, dc, in.ExpectedOutputFiles)
	if collectErr != nil {
		return runPythonFriendlyError(fmt.Sprintf("run_python: 收集输出文件失败: %v", collectErr)), nil
	}

	// Build result
	out := runPythonOutput{
		Files:      files,
		Stdout:     truncateString(execRes.Stdout, runPythonStdoutMaxBytes),
		Stderr:     truncateString(execRes.Stderr, runPythonStderrMaxBytes),
		ExitCode:   execRes.ExitCode,
		DurationMs: durationMs,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return runPythonFriendlyError("run_python: marshal output: " + err.Error()), nil
	}
	return b, nil
}

// ===========================================================================
// File I/O helpers
// ===========================================================================

// writeFileToSandbox writes data to an absolute path inside the sandbox
// container. It first tries sandbox.WriteFile (which uses CopyToContainer
// for /workdir/ prefixed paths), and falls back to a base64-encoded exec
// workaround for arbitrary paths.
func (t *runPythonTool) writeFileToSandbox(ctx context.Context, sess *sandbox.Session, containerPath string, data []byte, dc sandbox.DockerClient) error {
	// sandbox.WriteFile prepends /workdir/ to relative paths; use CopyToContainer directly
	// for absolute paths (e.g. /workspace/input/ or /workdir/run.py).
	return dc.CopyToContainer(ctx, sess.ContainerID, containerPath, strings.NewReader(string(data)))
}

// collectOutputFiles pulls files from /output/ inside the sandbox using
// CopyFromContainer to a temp dir, then uploads each to COS.
func (t *runPythonTool) collectOutputFiles(
	ctx context.Context,
	sess *sandbox.Session,
	dc sandbox.DockerClient,
	expectedFiles []string,
) ([]runPythonFileResult, error) {
	// List files in /output/
	lsRes, err := sandbox.ExecCommand(ctx, sess, "ls /output/ 2>/dev/null || true", dc)
	if err != nil {
		return nil, fmt.Errorf("ls /output/: %w", err)
	}
	rawNames := strings.Fields(lsRes.Stdout)

	// Filter to expected if provided
	var filenames []string
	if len(expectedFiles) > 0 {
		want := make(map[string]bool, len(expectedFiles))
		for _, f := range expectedFiles {
			want[f] = true
		}
		for _, name := range rawNames {
			if want[name] {
				filenames = append(filenames, name)
			}
		}
	} else {
		filenames = rawNames
	}

	// Cap at max output files
	if len(filenames) > runPythonMaxOutputFiles {
		filenames = filenames[:runPythonMaxOutputFiles]
	}

	if len(filenames) == 0 {
		return nil, nil
	}

	// Copy all output files to a temp dir on the host
	tmpDir, err := os.MkdirTemp("", "runpy-output-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// CopyFromContainer copies /output/ directory contents to tmpDir
	if err := dc.CopyFromContainer(ctx, sess.ContainerID, "/output/.", tmpDir); err != nil {
		return nil, fmt.Errorf("CopyFromContainer /output/: %w", err)
	}

	// Upload each file to COS
	userID := userIDFromContext(ctx)
	ts := time.Now().Format("20060102-150405")

	var results []runPythonFileResult
	for _, name := range filenames {
		localPath := filepath.Join(tmpDir, name)
		data, err := os.ReadFile(localPath)
		if err != nil {
			// File may not have been copied (CopyFromContainer is best-effort for individual files)
			continue
		}

		sanitized := sanitizeOutputFilename(name)
		objectKey := fmt.Sprintf("agent-outputs/%d/%s-py-%s", userID, ts, sanitized)

		ct := mime.TypeByExtension(filepath.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}

		rawURL, uploadErr := util.UploadBytesToCOS(ctx, objectKey, ct, data)
		if uploadErr != nil {
			return nil, fmt.Errorf("COS upload %s: %w", name, uploadErr)
		}
		if rawURL == "" {
			rawURL = fmt.Sprintf("/local-uploads/%s", objectKey)
		} else {
			const presignExpiry = 24 * 60 * 60 // 86400s per decision T4
			signed, signErr := util.GenerateSignedURL(ctx, objectKey, presignExpiry)
			if signErr == nil && signed != "" {
				rawURL = signed
			}
		}

		results = append(results, runPythonFileResult{
			Filename:  name,
			URL:       rawURL,
			SizeBytes: int64(len(data)),
		})
	}
	return results, nil
}

// downloadInputFile downloads a COS URL (or any HTTP URL) to memory.
// Returns at most 20 MiB (aligned with attachment limit).
func (t *runPythonTool) downloadInputFile(ctx context.Context, fileURL string) ([]byte, error) {
	// Try to get a presigned URL if it looks like a COS object key embedded URL
	objectKey := extractObjectKeyFromURL(fileURL)
	if objectKey != "" {
		const presignExpiry = 300 // 5 minutes for download
		signed, err := util.GenerateSignedURL(ctx, objectKey, presignExpiry)
		if err == nil && signed != "" {
			fileURL = signed
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http GET: status %d", resp.StatusCode)
	}

	const maxInputFileSizeBytes = 20 * 1024 * 1024 // 20 MiB
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 32768)
	totalRead := 0
	for {
		n, readErr := resp.Body.Read(tmp)
		if n > 0 {
			totalRead += n
			if totalRead > maxInputFileSizeBytes {
				return nil, fmt.Errorf("input file too large (> 20 MiB)")
			}
			buf = append(buf, tmp[:n]...)
		}
		if readErr != nil {
			break
		}
	}
	return buf, nil
}

// ===========================================================================
// String helpers
// ===========================================================================

// truncateString truncates s to at most maxBytes bytes. If truncated,
// appends "...[truncated]" to indicate content was cut.
func truncateString(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "...[truncated]"
}

// extractObjectKeyFromURL attempts to extract a COS object key from a URL.
// Returns "" if the URL does not look like a COS URL or cannot be parsed.
func extractObjectKeyFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	// COS URLs typically have path starting with /<objectKey>
	// Only presign if host contains tencentcos or cos.ap- pattern
	host := u.Host
	if !strings.Contains(host, "myqcloud.com") && !strings.Contains(host, "tencentcos") {
		return ""
	}
	// Path begins with "/" — strip it for the object key
	path := strings.TrimPrefix(u.Path, "/")
	return path
}

// runPythonFriendlyError returns a ToolResult containing {"error": "<msg>"} JSON.
// The LLM can read this and decide the next action.
func runPythonFriendlyError(msg string) ToolResult {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}
