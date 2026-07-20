package agent

import (
	"context"

	"numind-server/internal/numind/biz/agent/skills"
	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/biz/memory"
	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/retrieval/retrieve"

	"github.com/spf13/viper"
)

type platformToolFactory struct {
	// rag is the retrieval base service backing kb_search (T2.2: was salesrag.SalesRAGBiz;
	// now the domain-agnostic base — kb_search returns raw chunks, no in-tool answer generation).
	rag           *retrieve.Service
	ds            store.IStore
	skillRegistry skills.Registry       // disk platform skills; nil = load_skill serves DB-bound skills only
	skillPool     sandbox.SkillPool     // retained for forward compat (run_python uses sandbox.Pool, not SkillPool; load_skill does not need sandbox)
	creditService credit.ICreditService // agent-mode-billing T9: image_gen explicit Reserve/Reconcile; nil = no billing (tests)

	// lark workspace dependencies are injected as one complete set. LoadTools
	// fails closed to no Lark workspace tools when any side is absent.
	larkSkillReader SkillReadExecutor
	larkInspector   LarkInspector
	larkExecutor    LarkExecutor

	// larkProviderOverride is a test-only seam (feishu-integration T10): when set,
	// newLarkProvider returns it instead of building a Redis-backed feishu.Client
	// from config. Production always leaves it nil → the real lazy build runs.
	larkProviderOverride feishu.LarkAPIProvider

	// feishuConnectorOverride is a test-only seam (feishu-agent-connect R3): when
	// set, newFeishuConnector returns it instead of building a real orchestrator
	// from config/Redis. Production always leaves it nil → the real lazy build runs.
	feishuConnectorOverride feishuConnector
}

// SetFactoryCreditService injects the credit service into a platform tool factory
// (agent-mode-billing T9) so image_gen can Reserve/Reconcile real credits. No-op
// for non-platform factories. Call after construction, before LoadAll.
func SetFactoryCreditService(f ToolFactory, cs credit.ICreditService) {
	if pf, ok := f.(*platformToolFactory); ok {
		pf.creditService = cs
	}
}

// SetFactoryLarkWorkspaceExecutors injects the controlled skill reader and
// inspector/operation executor used by lark_skill_read, lark_inspect and
// lark_execute. The set is all-or-nothing: any nil clears all registrations.
func SetFactoryLarkWorkspaceExecutors(f ToolFactory, reader SkillReadExecutor, inspector LarkInspector, executor LarkExecutor) {
	pf, ok := f.(*platformToolFactory)
	if !ok {
		return
	}
	if reader == nil || inspector == nil || executor == nil {
		pf.larkSkillReader = nil
		pf.larkInspector = nil
		pf.larkExecutor = nil
		return
	}
	pf.larkSkillReader = reader
	pf.larkInspector = inspector
	pf.larkExecutor = executor
}

// NewPlatformToolFactory returns a ToolFactory that loads all platform built-in tools.
// rag is the retrieval base service used by kb_search (T2.2).
func NewPlatformToolFactory(rag *retrieve.Service, ds store.IStore) ToolFactory {
	return &platformToolFactory{rag: rag, ds: ds}
}

// NewPlatformToolFactoryWithSkills returns a ToolFactory that includes all platform
// built-in tools and wires a disk skill registry into load_skill (open-tools-skill-
// as-guidance merged read_skill into load_skill; single-loop progressive disclosure).
//
// reg is the disk platform skill registry. load_skill is registered REGARDLESS of reg
// (it always serves DB-bound skills); a non-nil reg additionally lets load_skill resolve
// disk SKILL.md skills (xlsx/docx/pptx/pdf-author). pool is accepted for API compat —
// load_skill reads SKILL.md from disk and does NOT use the sandbox, so a nil pool no
// longer prevents skill features from working. The agent uses run_python (which has its
// own sandbox.Pool wiring) to execute the Python the LLM authors from the guidance.
func NewPlatformToolFactoryWithSkills(
	rag *retrieve.Service,
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

// WithSkillRegistry injects a disk skills.Registry into the factory. load_skill is
// always registered by LoadTools; a non-nil registry lets it additionally resolve disk
// platform skills. The pool argument is retained for API compatibility but is no longer
// consulted (load_skill reads SKILL.md from disk).
//
// Call this after NewPlatformToolFactory but before LoadAll.
func (f *platformToolFactory) WithSkillRegistry(reg skills.Registry, pool sandbox.SkillPool) *platformToolFactory {
	f.skillRegistry = reg
	f.skillPool = pool
	return f
}

// SkillRegistry returns the configured skill registry (or nil). Used by runner.go
// to render the catalog block in the system prompt.
func (f *platformToolFactory) SkillRegistry() skills.Registry { return f.skillRegistry }

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
// Base tools (always present, ds=nil or ds!=nil): 18 tools, including load_skill
// (open-tools-skill-as-guidance: always registered; f.skillRegistry only controls
// whether disk platform skills are resolvable, not whether load_skill exists):
//
//	kb_search, document_generate, image_gen, bash_exec,
//	get_current_date, web_search, web_fetch, ask_user_question, file_read,
//	analyze_image, annotate_image, load_skill,
//	create_csv, create_html, create_json, create_text  (V1.5 output-skills task 4.2)
//	create_png_chart                                    (V1.5 output-skills task 4.3)
//	run_python                                          (V1.5 output-skills task 4.9)
//
// When f.ds is non-nil, memory_write + memory_read + xhs_note_list are appended.
//
// When both controlled Lark workspace dependencies are injected,
// lark_skill_read and lark_execute are appended. Partial injection registers
// neither tool. Legacy direct API/connect tools are intentionally not registered.
func (f *platformToolFactory) LoadTools(_ context.Context) ([]FullTool, []ToolMetadata, error) {
	var attStore store.IAgentAttachmentStore
	if f.ds != nil {
		attStore = f.ds.AgentAttachments()
	}
	tools := []FullTool{
		&kbSearchTool{retriever: f.rag},
		&documentGenerateTool{},
		&imageGenTool{ds: f.ds, creditService: f.creditService},
		&bashExecTool{},
		&getCurrentDateTool{},
		NewWebSearchToolFromConfig(),
		NewWebFetchTool(),
		NewAskUserQuestionTool(),
		NewFileReadToolWithStore(&documentParserImpl{}, &imageParserImpl{}, &textParserImpl{}, attStore),
		// V1.5 multimodal vision tools (task 1.4):
		// RequiresVision=false: these tools internally call a vision specialist model
		// (qwen3-vl-plus via profile.AttachmentVisionDescribe), so the main LLM does
		// NOT need vision capability — even single-modal models can call these tools.
		NewAnalyzeImageTool(attStore),
		NewAnnotateImageTool(),
		// open-tools-skill-as-guidance: load_skill — the unified skill-loading tool
		// (merges the former use_skill DB-skills tool + read_skill disk-skills tool).
		// Always registered; f.skillRegistry may be nil (then only DB-bound skills
		// resolve). IsEnabled=EnableSkills, so the runner's full-open loop exposes it
		// to every agent. Resolves DB-bound skills (via ctx turn state) DB-first, then
		// disk platform skills (SKILL.md). No binding-gated conditional needed.
		//
		// T4: when f.ds is wired, attach a marketplace snapshot reader so reference-
		// pointer skills (marketplace_id>0) load the publisher's CURRENT snapshot.
		f.newLoadSkillTool(),
		// V1.5 output-skills task 4.2: simple file generation tools (Layer 1, no sandbox).
		&createCSVTool{},
		&createHTMLTool{},
		&createJSONTool{},
		&createTextTool{},
		// agent-output-ux-followup3 BE-2: Markdown -> .docx deterministic fast path
		// (sandboxed fixed script; NOT no-sandbox like the others — IsEnabled gates
		// on EnableSandbox).
		&createDocxTool{},
		// V1.5 output-skills task 4.3: PNG chart tool (Layer 1, gonum/plot + go-chart/v2).
		&createPNGChartTool{},
		// V1.5 output-skills task 4.9: run_python (Layer 3, last-resort sandbox Python execution).
		&runPythonTool{},
	}
	metadata := []ToolMetadata{
		{ToolName: "kb_search", DisplayName: "知识库检索", Description: "Search the knowledge base.", Source: "platform", Category: "RAG"},
		{ToolName: "document_generate", DisplayName: "文档生成", Description: "[stub] Generate documents.", Source: "platform", Category: "生成"},
		{ToolName: "image_gen", DisplayName: "图像生成", Description: "[stub] Generate images.", Source: "platform", Category: "多媒体", RequiresSandbox: true},
		{ToolName: "bash_exec", DisplayName: "代码执行", Description: "[stub] Execute shell.", Source: "platform", Category: "代码", RiskLevel: "dangerous", RequiresSandbox: true},
		{ToolName: "get_current_date", DisplayName: "当前日期", Description: "Return today's date.", Source: "platform", Category: "查询"},
		{ToolName: "web_search", DisplayName: "网络搜索", Description: "Search the web for real-time information.", Source: "platform", RiskLevel: "safe", Category: "网络"},
		{ToolName: "web_fetch", DisplayName: "网页读取", Description: "Fetch a URL and return its contents as Markdown (JavaScript-rendered when the render service is available).", Source: "platform", RiskLevel: "moderate", Category: "网络"},
		{ToolName: "ask_user_question", DisplayName: "反问学员", Description: "Ask the user a clarifying question with structured options. Yields the run.", Source: "platform", RiskLevel: "safe", Category: "交互"},
		{ToolName: "file_read", DisplayName: "读取文件", Description: "Read an uploaded file's contents by URL.", Source: "platform", RiskLevel: "moderate", Category: "文件"},
		// V1.5 vision tools — RequiresVision intentionally absent (not in ToolMetadata struct);
		// both tools work with any main model because vision is handled internally.
		{ToolName: "analyze_image", DisplayName: "图像分析", Description: "Analyze an image in detail using a vision specialist model.", Source: "platform", RiskLevel: "moderate", Category: "视觉"},
		{ToolName: "annotate_image", DisplayName: "图像区域标注", Description: "Analyze specific regions within an image using a vision specialist model.", Source: "platform", RiskLevel: "moderate", Category: "视觉"},
		{ToolName: LoadSkillToolName, DisplayName: "加载技能", Description: "Load a skill's guidance into the conversation — DB-bound business skills or disk platform skills (xlsx-author / docx-author / pptx-author / pdf-from-html). Pair with run_python to execute the code a structured-file skill teaches.", Source: "platform", RiskLevel: "safe", Category: "技能"},
		// V1.5 output-skills task 4.2: simple file generation tools.
		{ToolName: "create_csv", DisplayName: "生成 CSV 文件", Description: "Generate a CSV file from tabular data.", Source: "platform", RiskLevel: "safe", Category: "文件生成"},
		{ToolName: "create_html", DisplayName: "生成 HTML 页面", Description: "Render an HTML page from content or a template.", Source: "platform", RiskLevel: "safe", Category: "文件生成"},
		{ToolName: "create_json", DisplayName: "生成 JSON 文件", Description: "Serialize data to a JSON file.", Source: "platform", RiskLevel: "safe", Category: "文件生成"},
		{ToolName: "create_text", DisplayName: "生成文本文件", Description: "Write plain text content to a .txt file.", Source: "platform", RiskLevel: "safe", Category: "文件生成"},
		// agent-output-ux-followup3 BE-2: Markdown -> .docx deterministic fast path.
		{ToolName: "create_docx", DisplayName: "生成 Word 文档（Markdown）", Description: "Generate a .docx Word document from Markdown (headings, paragraphs, lists, tables, inline images). For complex layouts use run_python + docx-author.", Source: "platform", RiskLevel: "safe", Category: "文件生成", RequiresSandbox: true},
		// V1.5 output-skills task 4.3: PNG chart tool.
		{ToolName: "create_png_chart", DisplayName: "图表生成（PNG）", Description: "Generate a static PNG chart from structured data.", Source: "platform", RiskLevel: "safe", Category: "可视化"},
		// V1.5 output-skills task 4.9: run_python (Layer 3 last-resort).
		{ToolName: "run_python", DisplayName: "Python 代码执行（文件生成）", Description: "Execute Python 3 code in an isolated sandbox to generate files. Use directly for long-tail formats; for xlsx/docx/pptx/pdf use the load_skill → run_python two-step (Layer 2).", Source: "platform", RiskLevel: "dangerous", Category: "代码", RequiresSandbox: true},
	}
	// Append memory tools only when a real store is available (nil guard preserves
	// the nil-ds unit test that expects exactly 18 base tools).
	if f.ds != nil {
		np := memory.NewNotepad(f.ds.UserGlobalMemories())
		tools = append(tools,
			NewMemoryWriteTool(np),
			NewMemoryReadTool(np),
			NewXhsNoteListTool(f.ds.Xhs()),
		)
		metadata = append(metadata,
			ToolMetadata{ToolName: "memory_write", DisplayName: "记忆写入", Description: "Write a long-term memory entry for the learner.", Source: "platform", Category: "记忆", RiskLevel: "moderate"},
			ToolMetadata{ToolName: "memory_read", DisplayName: "记忆读取", Description: "Read learner's long-term memory by key or kind.", Source: "platform", Category: "记忆"},
			ToolMetadata{ToolName: "xhs_note_list", DisplayName: "读取小红书选题库", Description: "Read the current authenticated user's captured Xiaohongshu notes with a stable cursor.", Source: "platform", Category: "小红书", RiskLevel: "safe", InputSchema: (&xhsNoteListTool{}).InputSchema()},
		)
	}

	if f.larkSkillReader != nil && f.larkInspector != nil && f.larkExecutor != nil {
		tools = append(tools,
			&larkSkillReadTool{executor: f.larkSkillReader},
			&larkInspectTool{inspector: f.larkInspector},
			&larkExecuteTool{executor: f.larkExecutor},
		)
		metadata = append(metadata,
			ToolMetadata{ToolName: "lark_skill_read", DisplayName: "读取飞书技能", Description: "Read one controlled page from the official embedded lark-cli skills.", Source: "platform", RiskLevel: "safe", Category: "飞书"},
			ToolMetadata{ToolName: "lark_inspect", DisplayName: "检查飞书工作区", Description: "Inspect current-user connection or command readiness without a business operation.", Source: "platform", RiskLevel: "safe", Category: "飞书"},
			ToolMetadata{ToolName: "lark_execute", DisplayName: "执行飞书工作区操作", Description: "Execute controlled Docs/Base/Wiki/Drive argv with platform-owned identity and policy.", Source: "platform", RiskLevel: "moderate", Category: "飞书"},
		)
	}
	return tools, metadata, nil
}

// newLarkProvider lazily builds the per-user feishu.LarkAPIProvider backing the
// three 飞书 tools. It returns nil (→ tools not registered) when:
//   - the store is absent (unit tests),
//   - features.feishu_integration.enabled is false, or
//   - a required dependency is missing (AES token key / Redis) — in which case it
//     logs and degrades to "no 飞书 tools" rather than half-wiring them.
//
// Dependencies (device-code): a lark-cli runner pinned to the persistent per-user
// home base (feishu.home_base) — it manages the user's token (decrypt/refresh moved
// into lark-cli, no cipher / Redis refresh lock needed here). The provider gates on
// the DB connected flag + lark-cli auth status. Built once per LoadTools.
//
//nolint:unused // Kept only for Task20's atomic legacy source removal.
func (f *platformToolFactory) newLarkProvider() feishu.LarkAPIProvider {
	// Test seam: a directly-injected provider short-circuits the config build.
	if f.larkProviderOverride != nil {
		return f.larkProviderOverride
	}
	if f.ds == nil {
		return nil
	}
	if !viper.GetBool("features.feishu_integration.enabled") {
		return nil
	}

	runner := f.buildLarkRunner()
	if runner == nil {
		return nil
	}
	client, err := feishu.NewClient(f.ds.ThirdPartyAccounts(), runner)
	if err != nil {
		log.Errorw("feishu tools: build client failed; 飞书 tools disabled", "err", err)
		return nil
	}
	return client
}

// buildLarkRunner builds the lark-cli runner pinned to the persistent per-user home
// base (G1-home). Returns nil (logged) on a build failure. Shared by newLarkProvider
// (ops) and newFeishuConnector (provisioning + device-code auth).
//
//nolint:unused // Kept only for Task20's atomic legacy source removal.
func (f *platformToolFactory) buildLarkRunner() *feishu.LarkCLIRunner {
	homeBase := viper.GetString("feishu.home_base")
	if homeBase == "" {
		homeBase = viper.GetString("feishu.lark_cli_home")
	}
	runner, err := feishu.NewLarkCLIRunner(viper.GetString("feishu.lark_cli_bin"), homeBase)
	if err != nil {
		log.Errorw("feishu: build lark-cli runner failed; 飞书 tools disabled", "err", err)
		return nil
	}
	return runner
}

// newFeishuConnector lazily builds the feishu.ConnectOrchestrator backing the
// feishu_connect tool. It returns nil (→ tool not registered) under the SAME
// preconditions as newLarkProvider: store absent, flag off, or a required
// dependency missing (AES key for the app-secret boundary / lark-cli runner).
//
// Device-code (G2-authorize): no signer / nonce / token exchanger / redirect_uri
// anymore. Dependencies mirror biz/feishu_adapter.go's buildFeishuService.
//
//nolint:unused // Kept only for Task20's atomic legacy source removal.
func (f *platformToolFactory) newFeishuConnector() feishuConnector {
	// Test seam: a directly-injected connector short-circuits the config build.
	if f.feishuConnectorOverride != nil {
		return f.feishuConnectorOverride
	}
	if f.ds == nil {
		return nil
	}
	if !viper.GetBool("features.feishu_integration.enabled") {
		return nil
	}

	cipher, err := crypto.NewCipher(viper.GetString("security.thirdparty_token_key"))
	if err != nil {
		log.Errorw("feishu_connect: build token cipher failed; tool disabled", "err", err)
		return nil
	}

	// G1-home: pin the runner to the PERSISTENT per-user home base so user homes
	// created via the feishu_connect agent tool survive a redeploy.
	runner := f.buildLarkRunner()
	if runner == nil {
		return nil
	}
	provisioner, err := feishu.NewProvisioner(cipher, runner)
	if err != nil {
		log.Errorw("feishu_connect: build provisioner failed; tool disabled", "err", err)
		return nil
	}

	orch, err := feishu.NewConnectOrchestrator(feishu.ConnectOrchestratorDeps{
		Store:      f.ds.ThirdPartyAccounts(),
		Starter:    provisioner,
		Poller:     provisioner,
		Authorizer: provisioner,
	})
	if err != nil {
		log.Errorw("feishu_connect: build connect orchestrator failed; tool disabled", "err", err)
		return nil
	}
	return orch
}

// newLoadSkillTool builds the load_skill tool, wiring a marketplace snapshot
// reader when a store is available (T4 reference-pointer resolution). When f.ds
// is nil (unit tests), falls back to the plain constructor (no marketplace read).
func (f *platformToolFactory) newLoadSkillTool() FullTool {
	if f.ds == nil {
		return NewLoadSkillTool(f.skillRegistry)
	}
	return NewLoadSkillToolWithMarketplace(f.skillRegistry, &marketplaceSnapshotReader{mp: f.ds.Marketplaces()})
}

// marketplaceSnapshotReader adapts store.IMarketplaceStore to the
// MarketplaceSnapshotReader interface. Reads BY marketplace_id ONLY (public row),
// never the publisher's private skill table (T4 cross-tenant guard).
type marketplaceSnapshotReader struct {
	mp store.IMarketplaceStore
}

func (r *marketplaceSnapshotReader) GetSnapshot(ctx context.Context, marketplaceID uint) (string, bool, bool) {
	if r.mp == nil {
		return "", false, false
	}
	row, err := r.mp.GetByID(ctx, marketplaceID)
	if err != nil || row == nil {
		return "", false, false
	}
	return row.SanitizedBodyMD, row.IsPublic, true
}

// Watch is a no-op in v1; dynamic reloading is deferred to a future task.
func (f *platformToolFactory) Watch(_ context.Context, _ func(diff ToolDiff)) error {
	return nil
}
