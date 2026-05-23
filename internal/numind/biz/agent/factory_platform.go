package agent

import (
	"context"

	"numind-server/internal/numind/biz/agent/skills"
	"numind-server/internal/numind/biz/memory"
	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/numind/store"
)

type platformToolFactory struct {
	rag           salesrag.SalesRAGBiz
	ds            store.IStore
	skillRegistry skills.Registry   // optional; nil = invoke_skill not registered
	skillPool     sandbox.SkillPool // optional; nil = invoke_skill not registered
}

// NewPlatformToolFactory returns a ToolFactory that loads all platform built-in tools.
func NewPlatformToolFactory(rag salesrag.SalesRAGBiz, ds store.IStore) ToolFactory {
	return &platformToolFactory{rag: rag, ds: ds}
}

// NewPlatformToolFactoryWithSkills returns a ToolFactory that includes all platform
// built-in tools plus the invoke_skill tool (V1.5 Track 4 task 4.4).
// Both reg and pool must be non-nil for invoke_skill to be registered; if either
// is nil the factory silently falls back to NewPlatformToolFactory behavior.
func NewPlatformToolFactoryWithSkills(
	rag salesrag.SalesRAGBiz,
	ds store.IStore,
	reg skills.Registry,
	pool sandbox.SkillPool,
) ToolFactory {
	return &platformToolFactory{
		rag:           rag,
		ds:            ds,
		skillRegistry: reg,
		skillPool:     pool,
	}
}

// WithSkillRegistry injects a skills.Registry and sandbox.SkillPool into the factory.
// When set, invoke_skill is appended to LoadTools. Both must be non-nil for
// invoke_skill to be registered; if either is nil the tool is silently omitted.
//
// Call this after NewPlatformToolFactory but before LoadAll.
func (f *platformToolFactory) WithSkillRegistry(reg skills.Registry, pool sandbox.SkillPool) *platformToolFactory {
	f.skillRegistry = reg
	f.skillPool = pool
	return f
}

// Compile-time assertion.
var _ ToolFactory = (*platformToolFactory)(nil)

func (f *platformToolFactory) FactoryID() string   { return "platform-builtin" }
func (f *platformToolFactory) Source() string      { return "platform" }
func (f *platformToolFactory) DisplayName() string { return "平台内置工具" }

// LoadTools instantiates all platform built-in FullTool instances together
// with their ToolMetadata for tool_definition upsert.
//
// Nil-safety: f.rag / f.ds may be nil in unit-test contexts (TestPlatformToolFactory_LoadTools
// passes nil to verify the tool list / metadata wiring without spinning up real biz/store).
// Tools constructed with nil deps will panic only if Execute is called; LoadTools itself
// must never panic.
//
// Base tools (always present, ds=nil or ds!=nil): 17 tools
//
//	kb_search, learner_data_query, document_generate, image_gen, bash_exec,
//	get_current_date, web_search, web_fetch, ask_user_question, file_read,
//	analyze_image, annotate_image,
//	create_csv, create_html, create_json, create_text  (V1.5 output-skills task 4.2)
//	create_png_chart                                    (V1.5 output-skills task 4.3)
//
// When f.skillRegistry and f.skillPool are non-nil, invoke_skill is appended:
// 18 base tools total. When f.ds is also non-nil, memory_write + memory_read are
// appended (20 tools with skills, 19 without).
func (f *platformToolFactory) LoadTools(_ context.Context) ([]FullTool, []ToolMetadata, error) {
	var usersGetter userByIDGetter
	var attStore store.IAgentAttachmentStore
	if f.ds != nil {
		usersGetter = f.ds.Users()
		attStore = f.ds.AgentAttachments()
	}
	tools := []FullTool{
		&kbSearchTool{rag: f.rag},
		&learnerDataQueryTool{users: usersGetter},
		&documentGenerateTool{},
		&imageGenTool{},
		&bashExecTool{},
		&getCurrentDateTool{},
		NewWebSearchToolFromConfig(),
		NewWebFetchTool(),
		NewAskUserQuestionTool(),
		NewFileReadTool(&pdfParserImpl{}, &imageParserImpl{}, &textParserImpl{}),
		// V1.5 multimodal vision tools (task 1.4):
		// RequiresVision=false: these tools internally call a vision specialist model
		// (qwen3-vl-plus via profile.AttachmentVisionDescribe), so the main LLM does
		// NOT need vision capability — even single-modal models can call these tools.
		NewAnalyzeImageTool(attStore),
		NewAnnotateImageTool(),
		// V1.5 output-skills task 4.2: simple file generation tools (Layer 1, no sandbox).
		&createCSVTool{},
		&createHTMLTool{},
		&createJSONTool{},
		&createTextTool{},
		// V1.5 output-skills task 4.3: PNG chart tool (Layer 1, gonum/plot + go-chart/v2).
		&createPNGChartTool{},
	}
	// V1.5 output-skills task 4.4: invoke_skill (Layer 2, sandbox-based skill framework).
	// Only registered when both skill registry and skill pool are available.
	// Nil guard preserves the nil-ds unit test that expects exactly 17 base tools.
	if f.skillRegistry != nil && f.skillPool != nil {
		var attStore store.IAgentAttachmentStore
		if f.ds != nil {
			attStore = f.ds.AgentAttachments()
		}
		tools = append(tools, NewInvokeSkillTool(f.skillRegistry, f.skillPool, attStore))
	}
	metadata := []ToolMetadata{
		{ToolName: "kb_search", DisplayName: "知识库检索", Description: "Search the knowledge base.", Source: "platform", Category: "RAG"},
		{ToolName: "learner_data_query", DisplayName: "学员档案", Description: "Query learner profile.", Source: "platform", Category: "查询", RiskLevel: "moderate"},
		{ToolName: "document_generate", DisplayName: "文档生成", Description: "[stub] Generate documents.", Source: "platform", Category: "生成"},
		{ToolName: "image_gen", DisplayName: "图像生成", Description: "[stub] Generate images.", Source: "platform", Category: "多媒体", RequiresSandbox: true},
		{ToolName: "bash_exec", DisplayName: "代码执行", Description: "[stub] Execute shell.", Source: "platform", Category: "代码", RiskLevel: "dangerous", RequiresSandbox: true},
		{ToolName: "get_current_date", DisplayName: "当前日期", Description: "Return today's date.", Source: "platform", Category: "查询"},
		{ToolName: "web_search", DisplayName: "网络搜索", Description: "Search the web for real-time information.", Source: "platform", RiskLevel: "safe", Category: "网络"},
		{ToolName: "web_fetch", DisplayName: "网页读取", Description: "Fetch a URL and return its contents as Markdown.", Source: "platform", RiskLevel: "moderate", Category: "网络"},
		{ToolName: "ask_user_question", DisplayName: "反问学员", Description: "Ask the user a clarifying question with structured options. Yields the run.", Source: "platform", RiskLevel: "safe", Category: "交互"},
		{ToolName: "file_read", DisplayName: "读取文件", Description: "Read an uploaded file's contents by URL.", Source: "platform", RiskLevel: "moderate", Category: "文件"},
		// V1.5 vision tools — RequiresVision intentionally absent (not in ToolMetadata struct);
		// both tools work with any main model because vision is handled internally.
		{ToolName: "analyze_image", DisplayName: "图像分析", Description: "Analyze an image in detail using a vision specialist model.", Source: "platform", RiskLevel: "moderate", Category: "视觉"},
		{ToolName: "annotate_image", DisplayName: "图像区域标注", Description: "Analyze specific regions within an image using a vision specialist model.", Source: "platform", RiskLevel: "moderate", Category: "视觉"},
		// V1.5 output-skills task 4.2: simple file generation tools.
		{ToolName: "create_csv", DisplayName: "生成 CSV 文件", Description: "Generate a CSV file from tabular data.", Source: "platform", RiskLevel: "safe", Category: "文件生成"},
		{ToolName: "create_html", DisplayName: "生成 HTML 页面", Description: "Render an HTML page from content or a template.", Source: "platform", RiskLevel: "safe", Category: "文件生成"},
		{ToolName: "create_json", DisplayName: "生成 JSON 文件", Description: "Serialize data to a JSON file.", Source: "platform", RiskLevel: "safe", Category: "文件生成"},
		{ToolName: "create_text", DisplayName: "生成文本文件", Description: "Write plain text content to a .txt file.", Source: "platform", RiskLevel: "safe", Category: "文件生成"},
		// V1.5 output-skills task 4.3: PNG chart tool.
		{ToolName: "create_png_chart", DisplayName: "图表生成（PNG）", Description: "Generate a static PNG chart from structured data.", Source: "platform", RiskLevel: "safe", Category: "可视化"},
	}
	// Append invoke_skill metadata when skill registry is available.
	if f.skillRegistry != nil && f.skillPool != nil {
		metadata = append(metadata, ToolMetadata{
			ToolName:        "invoke_skill",
			DisplayName:     "Skill 文件生成",
			Description:     "调用声明式 Skill 在沙箱中生成结构化文件（Excel/Word/PPT/PDF 等）。复杂格式（xlsx/docx/pptx/pdf）走此工具；简单格式用 create_csv/create_html/create_json/create_text/create_png_chart。",
			Source:          "platform",
			RiskLevel:       "moderate",
			Category:        "文件生成",
			RequiresSandbox: true,
		})
	}
	// Append memory tools only when a real store is available (nil guard preserves
	// the nil-ds unit test that expects exactly 17 tools).
	if f.ds != nil {
		np := memory.NewNotepad(f.ds.UserGlobalMemories())
		tools = append(tools,
			NewMemoryWriteTool(np),
			NewMemoryReadTool(np),
		)
		metadata = append(metadata,
			ToolMetadata{ToolName: "memory_write", DisplayName: "记忆写入", Description: "Write a long-term memory entry for the learner.", Source: "platform", Category: "记忆", RiskLevel: "moderate"},
			ToolMetadata{ToolName: "memory_read", DisplayName: "记忆读取", Description: "Read learner's long-term memory by key or kind.", Source: "platform", Category: "记忆"},
		)
	}
	return tools, metadata, nil
}

// Watch is a no-op in v1; dynamic reloading is deferred to a future task.
func (f *platformToolFactory) Watch(_ context.Context, _ func(diff ToolDiff)) error {
	return nil
}
