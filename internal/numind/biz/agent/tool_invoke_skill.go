package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"numind-server/internal/numind/biz/agent/skills"
	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ────────────────────────────────────────────────────────────────────────────────
// Constants
// ────────────────────────────────────────────────────────────────────────────────

const (
	// invokeSkillMaxInputFiles is the maximum number of input files per call.
	// Extra files are truncated to this number with a warning in the result.
	invokeSkillMaxInputFiles = 5

	// invokeSkillMaxInputFileSizeBytes is the per-input-file size limit (50 MiB).
	invokeSkillMaxInputFileSizeBytes = 50 * 1024 * 1024

	// invokeSkillStderrPreviewChars is the maximum number of stderr characters
	// included in the error JSON returned to the LLM.
	invokeSkillStderrPreviewChars = 500

	// invokeSkillMaxSkillMDBytes is the maximum number of bytes read from SKILL.md.
	// Content beyond this limit is truncated to prevent prompt explosion.
	invokeSkillMaxSkillMDBytes = 64 * 1024 // 64 KiB
)

// ────────────────────────────────────────────────────────────────────────────────
// SkillLLMCaller interface
// ────────────────────────────────────────────────────────────────────────────────

// SkillLLMCaller encapsulates the LLM call used to generate Python code from
// SKILL.md + user instructions. The interface allows mock injection in tests
// (the production implementation calls aiservice.Chat with profile.AgentRun).
//
// Decision T6 (DECISIONS.md): no new task profile — reuse profile.AgentRun.
type SkillLLMCaller interface {
	// GenerateCode instructs the LLM to produce a Python script guided by
	// skillMD (the SKILL.md content) and the caller's instructions.
	// inputFilePaths lists the filenames of files that have been injected into
	// the container's /workdir/input/ directory.
	// Returns the extracted code string (no markdown fencing).
	GenerateCode(ctx context.Context, skillMD, instructions string, inputFilePaths []string) (string, error)
}

// aiserviceSkillLLMCaller is the production SkillLLMCaller backed by aiservice.Chat.
type aiserviceSkillLLMCaller struct {
	skillName string
}

// GenerateCode implements SkillLLMCaller using aiservice.Chat(profile.AgentRun).
func (c *aiserviceSkillLLMCaller) GenerateCode(
	ctx context.Context,
	skillMD, instructions string,
	inputFilePaths []string,
) (string, error) {
	inputList := "(none)"
	if len(inputFilePaths) > 0 {
		inputList = "- " + strings.Join(inputFilePaths, "\n- ")
	}

	prompt := fmt.Sprintf(
		"你是代码生成专家。以下是 %s Skill 的使用指南：\n\n"+
			"--- SKILL.md 开始 ---\n%s\n--- SKILL.md 结束 ---\n\n"+
			"用户要求：\n%s\n\n"+
			"输入文件（已在沙箱 /workdir/input/ 目录，使用时直接读取）：\n%s\n\n"+
			"请生成完整的 Python 代码，将输出文件写到 /workdir/output/ 目录。\n"+
			"要求：\n"+
			"1. 代码直接可运行，不要有 markdown 包裹\n"+
			"2. 使用 SKILL.md 中列出的库\n"+
			"3. 异常处理完善（文件不存在、数据格式错误等）\n"+
			"4. 输出文件名需有意义（如 report_2026Q1.xlsx）\n",
		c.skillName, skillMD, instructions, inputList,
	)

	resp, err := aiservice.Chat(ctx, profile.AgentRun, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role:    aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{Text: prompt},
			},
		},
		Temperature: 0.2,
	})
	if err != nil {
		return "", fmt.Errorf("SkillLLMCaller.GenerateCode: aiservice.Chat: %w", err)
	}
	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("SkillLLMCaller.GenerateCode: LLM returned empty response")
	}

	return extractPythonCode(resp.Content), nil
}

// extractPythonCode extracts Python source from a possible markdown code block.
// If the response starts with ```python or ```, strips the fences. Otherwise
// returns the full content unchanged (assumed raw code).
func extractPythonCode(s string) string {
	s = strings.TrimSpace(s)
	// Strip ```python ... ``` or ``` ... ``` fences.
	for _, fence := range []string{"```python", "```"} {
		if strings.HasPrefix(s, fence) {
			s = strings.TrimPrefix(s, fence)
			// Trim leading newline after fence.
			if len(s) > 0 && s[0] == '\n' {
				s = s[1:]
			}
			// Strip trailing ```.
			if idx := strings.LastIndex(s, "```"); idx >= 0 {
				s = s[:idx]
			}
			return strings.TrimSpace(s)
		}
	}
	return s
}

// ────────────────────────────────────────────────────────────────────────────────
// invoke_skill FullTool
// ────────────────────────────────────────────────────────────────────────────────

// invokeSkillTool is the invoke_skill FullTool implementation.
// It orchestrates the full skill execution lifecycle:
//  1. Validate arguments.
//  2. Look up skill in the Registry.
//  3. Acquire a sandbox SkillSession (AcquireForSkill).
//  4. Download + inject input files.
//  5. Read SKILL.md from the container.
//  6. Call LLM to generate Python code.
//  7. Write code to the container as /workdir/skill_run.py.
//  8. Execute python3 /workdir/skill_run.py.
//  9. Collect outputs from /workdir/output/.
//  10. Write agent_attachment rows.
//  11. Return result JSON to the LLM.
type invokeSkillTool struct {
	BaseTool
	registry  skills.Registry
	pool      sandbox.SkillPool
	llmCaller SkillLLMCaller
	attStore  store.IAgentAttachmentStore
}

// Compile-time assertion.
var _ FullTool = (*invokeSkillTool)(nil)

// NewInvokeSkillTool constructs a new invokeSkillTool wired with the provided
// dependencies. All parameters may be nil in unit tests that only need to
// call IsEnabled or inspect the tool's metadata; Execute will fail gracefully
// if called with nil deps.
func NewInvokeSkillTool(
	registry skills.Registry,
	pool sandbox.SkillPool,
	attStore store.IAgentAttachmentStore,
) *invokeSkillTool {
	return &invokeSkillTool{
		registry: registry,
		pool:     pool,
		attStore: attStore,
		// Production LLM caller — set per-skill at registration time below.
		// We override via llmCallerFor in Execute, so the field here is nil and
		// is set by withLLMCaller (used only in tests).
	}
}

// withLLMCaller returns a copy of the tool with the given SkillLLMCaller injected.
// Used in tests to inject mock callers.
func (t *invokeSkillTool) withLLMCaller(c SkillLLMCaller) *invokeSkillTool {
	copy := *t
	copy.llmCaller = c
	return &copy
}

// ── Metadata ─────────────────────────────────────────────────────────────────

func (t *invokeSkillTool) Name() string { return "invoke_skill" }

func (t *invokeSkillTool) Description() string {
	return "invoke_skill：调用声明式 Skill 在沙箱中生成结构化文件（Excel/Word/PPT/PDF 等）。" +
		"使用场景：需要生成复杂格式文件（.xlsx/.docx/.pptx/.pdf）时调用此工具；" +
		"简单格式（CSV/HTML/JSON/纯文本/PNG 图表）应优先使用 create_csv / create_html / " +
		"create_json / create_text / create_png_chart（无需沙箱，更快）。" +
		"skill_name 是已注册的 skill ID（如 xlsx-author、docx-author、pptx-author、pdf-from-html）；" +
		"instructions 用自然语言描述要生成什么内容；" +
		"input_files 是可选的输入文件 URL 列表（来自用户上传或之前工具调用的产物）。"
}

func (t *invokeSkillTool) UserFacingName() string { return "Skill 文件生成" }
func (t *invokeSkillTool) NarrationVerb() string  { return "生成" }
func (t *invokeSkillTool) IsDestructive() bool    { return false }
func (t *invokeSkillTool) IsReadOnly() bool       { return false }

// IsEnabled returns true only when both sandbox and skills are enabled.
// Decision spec §7: requires EnableSandbox && EnableSkills.
func (t *invokeSkillTool) IsEnabled(cfg ToolConfig) bool {
	return cfg.EnableSandbox && cfg.EnableSkills
}

// ── Execute ───────────────────────────────────────────────────────────────────

// invokeSkillArgs is the JSON argument schema for invoke_skill.
type invokeSkillArgs struct {
	SkillName    string   `json:"skill_name"`
	Instructions string   `json:"instructions"`
	InputFiles   []string `json:"input_files,omitempty"`
}

// invokeSkillResult is the success JSON returned to the LLM.
type invokeSkillResult struct {
	Status     string            `json:"status"`
	Files      []invokeSkillFile `json:"files,omitempty"`
	DurationMS int64             `json:"duration_ms,omitempty"`
	Warning    string            `json:"warning,omitempty"`
}

// invokeSkillFile describes a single output file.
type invokeSkillFile struct {
	Filename  string `json:"filename"`
	MimeType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
	URL       string `json:"url"`
}

// invokeSkillError is the error JSON returned to the LLM (not a Go error).
type invokeSkillError struct {
	Status        string `json:"status"`
	Error         string `json:"error"`
	StderrPreview string `json:"stderr_preview,omitempty"`
	SkillName     string `json:"skill_name,omitempty"`
	Warning       string `json:"warning,omitempty"`
}

func skillErrorJSON(skillName, msg, stderr, warning string) ToolResult {
	e := invokeSkillError{
		Status:    "error",
		Error:     msg,
		SkillName: skillName,
		Warning:   warning,
	}
	if len(stderr) > invokeSkillStderrPreviewChars {
		e.StderrPreview = stderr[:invokeSkillStderrPreviewChars]
	} else if stderr != "" {
		e.StderrPreview = stderr
	}
	b, _ := json.Marshal(e)
	return b
}

func skillSuccessJSON(files []invokeSkillFile, durationMS int64, warning string) ToolResult {
	r := invokeSkillResult{
		Status:     "ok",
		Files:      files,
		DurationMS: durationMS,
		Warning:    warning,
	}
	b, _ := json.Marshal(r)
	return b
}

// Execute runs the full 11-step invoke_skill lifecycle.
// It always returns a ToolResult (error JSON is LLM-readable) and surfaces a
// non-nil Go error only for infrastructure failures that should mark the audit
// row as "failed".
func (t *invokeSkillTool) Execute(ctx context.Context, input ToolInput) (ToolResult, error) {
	start := time.Now()

	// ── Step 1: Parse and validate arguments ─────────────────────────────────
	var args invokeSkillArgs
	if err := json.Unmarshal(input, &args); err != nil {
		return skillErrorJSON("", "invoke_skill: invalid JSON input", "", ""), nil
	}
	if args.SkillName == "" {
		return skillErrorJSON("", "invoke_skill: skill_name is required", "", ""), nil
	}
	if args.Instructions == "" {
		return skillErrorJSON(args.SkillName, "invoke_skill: instructions are required", "", ""), nil
	}

	// Truncate input_files to max allowed, tracking warning.
	var warning string
	if len(args.InputFiles) > invokeSkillMaxInputFiles {
		args.InputFiles = args.InputFiles[:invokeSkillMaxInputFiles]
		warning = fmt.Sprintf("only first %d input files were used", invokeSkillMaxInputFiles)
		log.Warnw("invoke_skill: input_files truncated", "skill", args.SkillName, "count", invokeSkillMaxInputFiles)
	}

	// ── Step 2: Look up skill in registry ────────────────────────────────────
	if t.registry == nil {
		return skillErrorJSON(args.SkillName, "skill registry not available — skills_root may not be configured", "", warning), nil
	}
	entry, err := t.registry.Get(args.SkillName)
	if err != nil {
		available := skills.SkillNames(t.registry)
		return skillErrorJSON(
			args.SkillName,
			fmt.Sprintf("skill %q not found. available: %s", args.SkillName, available),
			"", warning,
		), nil
	}

	// ── Step 3: Acquire sandbox SkillSession ─────────────────────────────────
	if t.pool == nil {
		return skillErrorJSON(args.SkillName, "sandbox pool not available", "", warning), nil
	}
	userID := userIDFromContext(ctx)
	timeoutDur := time.Duration(entry.Manifest.EffectiveMaxRuntime()) * time.Second
	acquireCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	sess, err := t.pool.AcquireForSkill(acquireCtx, args.SkillName, userID)
	if err != nil {
		log.Warnw("invoke_skill: AcquireForSkill failed",
			"skill", args.SkillName, "userID", userID, "error", err)
		return skillErrorJSON(args.SkillName, "sandbox unavailable, retry later", "", warning), nil
	}
	// ── Step 4: defer ReturnSkillSession — container always returned ──────────
	defer func() {
		if returnErr := t.pool.ReturnSkillSession(sess, 0, ""); returnErr != nil {
			log.Warnw("invoke_skill: ReturnSkillSession failed (non-fatal)",
				"skill", args.SkillName, "containerID", sess.ContainerID, "error", returnErr)
		}
	}()

	dc := dockerClientForCurrentCall(ctx)
	if dc == nil {
		return skillErrorJSON(args.SkillName, "sandbox docker client not initialized", "", warning), nil
	}

	// ── Step 5: Download and inject input files ───────────────────────────────
	var injectedFilenames []string
	for _, fileURL := range args.InputFiles {
		filename, data, dlErr := downloadInputFile(ctx, fileURL)
		if dlErr != nil {
			log.Warnw("invoke_skill: input file download failed, skipping",
				"url", fileURL, "error", dlErr)
			continue
		}
		if copyErr := t.pool.CopyFileIn(ctx, sess, filename, data); copyErr != nil {
			log.Warnw("invoke_skill: CopyFileIn failed, skipping file",
				"filename", filename, "error", copyErr)
			continue
		}
		injectedFilenames = append(injectedFilenames, filename)
	}

	// ── Step 6: Read SKILL.md from container ─────────────────────────────────
	skillMD := readSkillMD(ctx, sess, args.SkillName, entry, dc)

	// ── Step 7: Call LLM to generate Python code ─────────────────────────────
	caller := t.llmCaller
	if caller == nil {
		caller = &aiserviceSkillLLMCaller{skillName: args.SkillName}
	}
	pythonCode, codeErr := caller.GenerateCode(ctx, skillMD, args.Instructions, injectedFilenames)
	if codeErr != nil {
		log.Warnw("invoke_skill: LLM code generation failed",
			"skill", args.SkillName, "error", codeErr)
		return skillErrorJSON(args.SkillName, "LLM failed to generate code: "+codeErr.Error(), "", warning), nil
	}
	if strings.TrimSpace(pythonCode) == "" {
		log.Warnw("invoke_skill: LLM returned empty code",
			"skill", args.SkillName, "instructions_len", len(args.Instructions))
		return skillErrorJSON(args.SkillName, "LLM failed to generate code (empty response)", "", warning), nil
	}

	// ── Step 8: Write Python code to container and execute ───────────────────
	// Use WriteFile (via sandbox.WriteFile) to inject skill_run.py into /workdir/.
	// This avoids shell escape issues with -c execution.
	if writeErr := sandbox.WriteFile(ctx, sess.Session, "skill_run.py", []byte(pythonCode), dc); writeErr != nil {
		log.Warnw("invoke_skill: write skill code to container failed",
			"skill", args.SkillName, "error", writeErr, "code_len", len(pythonCode))
		return skillErrorJSON(args.SkillName, "failed to write skill code to container: "+writeErr.Error(), "", warning), nil
	}

	execResult, execErr := sandbox.ExecCommand(
		ctx, sess.Session,
		"python3 /workdir/skill_run.py",
		dc,
	)
	if execErr != nil {
		// Observability: the stderr/exit here were previously only handed to the
		// LLM (which paraphrases them, e.g. "环境初始化遇到问题"), leaving zero
		// server-side trace. Log the full picture so recurring first-attempt
		// failures (2026-05-29 PPT retry) are diagnosable: the Python traceback
		// (stderr) plus a preview of the LLM-generated code that produced it.
		log.Warnw("invoke_skill: sandbox execution error",
			"skill", args.SkillName,
			"error", execErr,
			"exit_code", execResult.ExitCode,
			"stderr", truncateRunes(execResult.Stderr, 2000),
			"stdout", truncateRunes(execResult.Stdout, 500),
			"code_len", len(pythonCode),
			"code_preview", truncateRunes(pythonCode, 1200),
		)
		return skillErrorJSON(args.SkillName,
			"sandbox execution failed: "+execErr.Error(),
			execResult.Stderr, warning,
		), execErr
	}
	if execResult.ExitCode != 0 {
		// The common failure mode: LLM-generated Python raised an exception
		// (exit 1). stderr holds the traceback — log it + the code preview so we
		// can see exactly which line/library call failed.
		log.Warnw("invoke_skill: skill code exited non-zero",
			"skill", args.SkillName,
			"exit_code", execResult.ExitCode,
			"stderr", truncateRunes(execResult.Stderr, 2000),
			"stdout", truncateRunes(execResult.Stdout, 500),
			"code_len", len(pythonCode),
			"code_preview", truncateRunes(pythonCode, 1200),
		)
		return skillErrorJSON(args.SkillName,
			fmt.Sprintf("skill ran but returned exit code %d", execResult.ExitCode),
			execResult.Stderr, warning,
		), nil
	}

	// ── Step 9: Collect outputs ───────────────────────────────────────────────
	outputs, collectErr := t.pool.CollectOutputs(ctx, sess, userID)
	if collectErr != nil {
		log.Warnw("invoke_skill: CollectOutputs error (may have partial results)",
			"skill", args.SkillName, "error", collectErr)
		// Fall through — outputs may be non-empty even on partial error.
	}
	if len(outputs) == 0 {
		log.Warnw("invoke_skill: skill ran but produced no output files",
			"skill", args.SkillName,
			"exit_code", execResult.ExitCode,
			"stdout", truncateRunes(execResult.Stdout, 500),
			"stderr", truncateRunes(execResult.Stderr, 1000),
			"code_len", len(pythonCode),
		)
		return skillErrorJSON(
			args.SkillName,
			"skill ran successfully but produced no output files",
			execResult.Stderr, warning,
		), nil
	}

	// ── Step 10: Write agent_attachment rows ──────────────────────────────────
	durationMS := time.Since(start).Milliseconds()
	files := make([]invokeSkillFile, 0, len(outputs))
	for _, out := range outputs {
		files = append(files, invokeSkillFile{
			Filename:  out.Filename,
			MimeType:  out.MimeType,
			SizeBytes: out.SizeBytes,
			URL:       out.COSURL,
		})
		if t.attStore != nil {
			att := &model.AgentAttachment{
				UserID:   userID,
				URL:      out.COSURL,
				Filename: out.Filename,
				MimeType: out.MimeType,
				Size:     out.SizeBytes,
				Modality: "generated",
			}
			if attErr := t.attStore.Create(ctx, att); attErr != nil {
				log.Warnw("invoke_skill: agent_attachment Create failed (non-fatal)",
					"skill", args.SkillName, "filename", out.Filename, "error", attErr)
			}
		}
	}

	// ── Step 11: Return success JSON ──────────────────────────────────────────
	log.Infow("invoke_skill: success",
		"skill", args.SkillName,
		"userID", userID,
		"files", len(files),
		"duration_ms", durationMS,
	)
	return skillSuccessJSON(files, durationMS, warning), nil
}

// ────────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────────

// downloadInputFile downloads a file from the given URL.
// Returns (filename, data, error). The filename is derived from the URL path.
func downloadInputFile(ctx context.Context, rawURL string) (string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("downloadInputFile: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("downloadInputFile: GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("downloadInputFile: HTTP %d for %s", resp.StatusCode, rawURL)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, invokeSkillMaxInputFileSizeBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("downloadInputFile: read body: %w", err)
	}
	if int64(len(data)) > invokeSkillMaxInputFileSizeBytes {
		return "", nil, fmt.Errorf("downloadInputFile: file exceeds %d bytes", invokeSkillMaxInputFileSizeBytes)
	}

	// Extract filename from URL path.
	filename := extractFilenameFromURL(rawURL)
	return filename, data, nil
}

// extractFilenameFromURL derives a safe filename from a URL, falling back to
// "input_file" when the URL path has no recognisable filename component.
// It parses the URL properly to avoid treating the hostname as a path segment.
// Percent-encoded characters in the path are sanitized as-is (not decoded),
// so "%20" becomes "_20" rather than a space followed by "_".
func extractFilenameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "input_file"
	}
	// EscapedPath() returns the path with percent-encoding preserved (not decoded),
	// which ensures "%20" is kept raw and sanitized to "_20" rather than decoded to " ".
	rawPath := u.EscapedPath()
	parts := strings.Split(rawPath, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if part != "" {
			return sanitizeOutputFilename(part)
		}
	}
	return "input_file"
}

// readSkillMD attempts to read SKILL.md from inside the container via
// docker exec cat. On failure (container not started, file missing, etc.)
// it falls back to the skill's manifest.description — so the LLM always
// receives some guidance.
func readSkillMD(
	ctx context.Context,
	sess *sandbox.SkillSession,
	skillName string,
	entry *skills.SkillEntry,
	dc sandbox.DockerClient,
) string {
	containerPath := fmt.Sprintf("/skills/%s/SKILL.md", skillName)
	res, err := dc.Exec(ctx, sess.ContainerID, []string{"cat", containerPath}, sandbox.ExecOpts{
		Timeout: 10 * time.Second,
		Workdir: "/workdir",
		User:    sess.Config.UserSpec,
	})
	if err != nil || res.ExitCode != 0 || res.Stdout == "" {
		log.Warnw("invoke_skill: SKILL.md read failed, degrading to manifest.description",
			"skill", skillName, "error", err, "exitCode", res.ExitCode)
		return entry.Manifest.Description
	}

	content := res.Stdout
	if len(content) > invokeSkillMaxSkillMDBytes {
		content = content[:invokeSkillMaxSkillMDBytes]
		log.Warnw("invoke_skill: SKILL.md truncated to 64KiB", "skill", skillName)
	}
	return content
}
