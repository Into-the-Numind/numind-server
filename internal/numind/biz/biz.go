package biz

//go:generate mockgen -destination mock_biz.go -package biz github.com/marmotedu/miniblog/internal/miniblog/biz IBiz

import (
	"context"
	"path/filepath"
	"time"

	"numind-server/internal/numind/biz/agent"
	agentatt "numind-server/internal/numind/biz/agent/attachment"
	"numind-server/internal/numind/biz/agent/budgetgate"
	"numind-server/internal/numind/biz/agent/compliancegate"
	"numind-server/internal/numind/biz/agent/search"
	"numind-server/internal/numind/biz/agent/skills"
	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/attachment"
	"numind-server/internal/numind/biz/budget"
	chatbotbiz "numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/biz/compliance"
	"numind-server/internal/numind/biz/config"
	"numind-server/internal/numind/biz/credit"
	customerbiz "numind-server/internal/numind/biz/customer"
	kbbiz "numind-server/internal/numind/biz/knowledgebase"
	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/biz/memory"
	"numind-server/internal/numind/biz/monitor"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/numind/biz/payment"
	"numind-server/internal/numind/biz/permission"
	permvalidators "numind-server/internal/numind/biz/permission/validators"
	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/seed"
	salesragservice "numind-server/internal/numind/biz/salesrag/service"
	"numind-server/internal/numind/biz/sandbox"
	skillbiz "numind-server/internal/numind/biz/skill"
	skillartifact "numind-server/internal/numind/biz/skill/artifact"
	sopbiz "numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/log"
	docparser "numind-server/internal/pkg/parser"
	"numind-server/internal/pkg/pricing"
	redispkg "numind-server/internal/pkg/redis"
	radapter "numind-server/internal/pkg/retrieval/adapter"
	ingest "numind-server/internal/pkg/retrieval/ingest"
	"numind-server/internal/pkg/retrieval/port"
	"numind-server/internal/pkg/retrieval/retrieve"

	goredis "github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

// B 是 biz 层全局单例，镜像 store.S 模式。middleware/cron 等 wire 不便注入的代码路径
// 可通过此变量调 biz 函数。在 NewBiz 时初始化，不应被外部直接重置。
var B IBiz

// IBiz 定义了 Biz 层需要实现的方法.
type IBiz interface {
	Users() user.UserBiz
	Ali() ali.AliBiz
	Volc() volc.VolcBiz
	Configs() config.ConfigBiz
	Sop() sopbiz.ISopBiz                          // SOP服务
	Customers() customerbiz.ICustomerBiz          // 客户管理服务
	SalesRAG() salesrag.SalesRAGBiz               // 销售 RAG 服务
	Credit() credit.ICreditBiz                    // 积分服务
	CreditService() credit.ICreditService         // credits-system ICreditService 统一入口
	Pricing() pricing.ICalculator                 // pricing 同步成本计算
	Payment() payment.IPaymentBiz                 // 支付服务
	Monitor() monitor.IMonitorBiz                 // 博主监控服务
	KnowledgeBase() kbbiz.IKnowledgeBaseBiz       // 知识库服务
	Chatbot() chatbotbiz.IChatbotBiz              // 智能体服务
	LLMRouter() *llmrouter.Router                 // LLM 路由服务
	Agents() agent.AgentRunner                    // Agent Runtime（agent-mode #2）
	AgentTools() agent.AgentToolRegistry          // Agent Tool Registry（agent-mode #3）
	Skill() skillbiz.Service                      // Agent Skill CRUD（#5/14 skill-system）
	StudentQuery() *agent.StudentQueryService     // Student-facing agent query (#14 follow-up ALPHA)
	StudentRun() *agent.StudentRunService         // Student-facing run lifecycle (#14 BETA)
	Attachment() *attachment.UploadService        // File attachment upload (#14 BETA)
	AttachmentFallback() agentatt.FallbackService // Async fallback generation (V1.5 task 1.2)
	MemoryCadence() *memory.CadenceService        // Task 3.6 dialectic cadence gate (Task 3.7 caller)
	SearchService() search.Service                // Task 3.5 FULLTEXT search (router.go consumer)
}

// 确保 biz 实现了 IBiz 接口.
var _ IBiz = (*biz)(nil)

// biz 是 IBiz 的一个具体实现.
type biz struct {
	ds                store.IStore
	sopService        sopbiz.ISopBiz
	salesRAGService   salesrag.SalesRAGBiz
	credit            credit.ICreditBiz
	creditService     credit.ICreditService
	pricing           pricing.ICalculator
	payment           payment.IPaymentBiz
	monitorService    monitor.IMonitorBiz
	kbService         kbbiz.IKnowledgeBaseBiz
	chatbotService    chatbotbiz.IChatbotBiz
	llmRouterSvc      *llmrouter.Router
	agentRunner       agent.AgentRunner
	agentToolRegistry agent.AgentToolRegistry
	permissionGate    *permission.PermissionGate // #6 agent-mode-permission-pipeline
	complianceGate    compliance.ComplianceGate  // #13 agent-mode-compliance-3layer
	complianceAudit   *compliance.AuditLogger    // #13 agent-mode-compliance-3layer (Stop on shutdown)
	memoryExtractor   *memory.ExtractorService   // Task 3.3 LLM extraction (Stop on shutdown)
	memoryCadence     *memory.CadenceService     // Task 3.6 dialectic cadence gate (read-only)
	memoryDialectic   memory.DialecticService    // Task 3.7 Layer A dialectic insight provider (background goroutine-based)
	memoryTemporal    memory.TemporalService     // Task 3.8 temporal digest injector (per-turn read-only, 4 granularities)
	memoryDigestCron  *memory.CronRunner         // Task 3.8 cron scheduler (4 jobs: daily/weekly/monthly/quarterly); Stop on shutdown
	searchService     search.Service             // Task 3.5 FULLTEXT ngram search (also wired into AgentRunner via WithSearchService)
	studentQuerySvc   *agent.StudentQueryService // #14 follow-up ALPHA student-facing queries
	studentRunSvc     *agent.StudentRunService   // #14 BETA student-facing run lifecycle
	attachFallbackSvc agentatt.FallbackService   // V1.5 multimodal fallback (task 1.2)
	uploadSvc         *attachment.UploadService  // wired with fallback (V1.5 task 1.2)
}

// NewBiz 创建一个 IBiz 类型的实例.
func NewBiz(ds store.IStore) *biz {
	// MembershipService 用于 credits-mode 用户的 cycle/booster/trial 余额读写。
	// router.go 也会自己构造一份（包同样的 DB），两份实例无状态争用。
	membershipSvc := membership.NewMembershipService(ds.DB())
	creditBiz := credit.NewCreditBiz(ds)
	// Wire membershipSvc into creditBiz so CanPerformAIOperation reads new tables.
	credit.InjectCreditBizMembershipSvc(creditBiz, membershipSvc)
	// pricing.NewCalculator takes a local PricingStore interface (subset of full store).
	// ds.Billing() structurally satisfies PricingStore (per Track B design).
	pricingCalc := pricing.NewCalculator(ds.Billing())
	creditSvc := credit.NewCreditService(ds, creditBiz, pricingCalc, membershipSvc)
	b := &biz{
		ds:            ds,
		credit:        creditBiz,
		creditService: creditSvc,
		pricing:       pricingCalc,
		payment:       payment.NewPaymentBiz(ds, creditBiz),
		llmRouterSvc:  llmrouter.New(ds),
		// agentRunner / agentToolRegistry initialized after salesRAGService (line ~215)
	}

	// 创建 ConfigReader，用于从 Redis → MySQL → Viper 读取配置
	_ = config.NewConfigReader(b.Configs())

	// 初始化SOP服务。LLM 调用统一走 aiservice Gateway，不再需要 LLMRouter 参数。
	//
	// Phase 2 Task 2.0/2.1: pricingCalc + creditSvc 已在本函数顶部构造（注入 b.pricing /
	// b.creditService 字段）。此处仅用于 sopBiz 的 fluent WithCreditService setter。
	sopExecutor := sopbiz.NewSopExecutor(b.ds)
	b.sopService = sopbiz.NewSopBiz(b.ds, sopExecutor, b.credit).
		WithCreditService(creditSvc, pricingCalc)

	// 初始化销售 RAG 服务
	// 向量库支持 sqlitevec（默认）、dashvector（回退兼容）、memory（测试）
	var vStore port.VectorStore

	// Define Embedder via AI Gateway (profile.SalesragEmbed).
	// Context arriving here from pipeline.worker() already carries
	// aismw.WithUserID + aiservice.WithSkipLegacyBilling.
	// Dimension=2048 is fixed by the existing DashVector collection
	// `sales_rag_prod` schema; must match task_profile.requirements.dimension
	// and ai_service.capability_json.dimension for the routed service.
	embedder := radapter.NewGatewayEmbedder(profile.SalesragEmbed, 2048)

	vectorStoreType := viper.GetString("salesrag.vector_store.type")
	if vectorStoreType == "" {
		vectorStoreType = "sqlitevec" // 默认使用 sqlite-vec
	}

	switch vectorStoreType {
	case "sqlitevec":
		vecDBPath := viper.GetString("salesrag.vector_store.path")
		if vecDBPath == "" {
			// 基于 resource.image_path 计算默认路径
			imagePath := viper.GetString("resource.image_path")
			parentDir := filepath.Dir(imagePath) // 移除 upload
			if filepath.Base(parentDir) == "image" {
				baseDir := filepath.Dir(parentDir)
				vecDBPath = filepath.Join(baseDir, "sales_vector.db")
			} else {
				vecDBPath = filepath.Join(parentDir, "sales_vector.db")
			}
		}
		var vecErr error
		vStore, vecErr = radapter.NewSQLiteVecStore(vecDBPath, embedder)
		if vecErr != nil {
			log.Errorw("Failed to initialize SQLiteVecStore, falling back to MemoryStore", "error", vecErr, "path", vecDBPath)
			vStore = radapter.NewMemoryStore()
		} else {
			log.Infow("Initialized SQLiteVecStore", "path", vecDBPath)
		}
	case "dashvector":
		// 保留向后兼容，迁移期间可用
		dashEndpoint := viper.GetString("ali.dashvector.endpoint")
		dashApiKey := viper.GetString("ali.dashvector.api_key")
		if dashApiKey == "" {
			dashApiKey = viper.GetString("ali.api_key")
		}
		dashCollection := viper.GetString("ali.dashvector.collection")
		if dashCollection == "" {
			dashCollection = "sales_rag"
		}
		vStore = radapter.NewDashVectorStore(dashEndpoint, dashApiKey, dashCollection, embedder)
		log.Infow("Initialized DashVector store", "endpoint", dashEndpoint, "collection", dashCollection)
	case "memory":
		vStore = radapter.NewMemoryStore()
		log.Infow("Initialized MemoryStore (testing only)")
	default:
		log.Warnw("Unknown vector store type, falling back to MemoryStore", "type", vectorStoreType)
		vStore = radapter.NewMemoryStore()
	}

	// 初始化 LLM 意图路由器（V2: 使用 DMXAPI qwen-turbo-latest）
	llmRouter := adapter.NewLLMRouter()

	// Initialize Pipeline Components
	parser := docparser.NewDocumentParser()
	// 使用增强版切分器（支持中文分词、语义边界、100字符重叠、Markdown分级）
	splitter := ingest.NewCompatibilitySplitter(ingest.SplitterConfig{
		MaxChunkSize: 1000,
		MinChunkSize: 200,
	})
	tagger := ingest.NewContentTagger()

	// Initialize Ingestion Pipeline (托管模式下不需要传 embedder)
	pipeline := ingest.NewIngestionPipeline(parser, splitter, tagger, b.ds.KnowledgeDocuments(), vStore, b.ds.KnowledgeChunks())

	// 业务逻辑实现（使用 LLMRouter）
	salesRAGSvc := salesragservice.NewSalesRAGService(vStore, llmRouter)

	// 创建 SalesSessionStore
	salesSessionStore := store.NewSalesSessionStore(b.ds.DB())

	// Phase 2 Task 2.2 wiring: 传入 creditSvc + pricingCalc 激活 SalesRAG 积分扣减（prod 漏洞修复）
	// reg: salesrag biz uses ResolveTask("salesrag.chat") so CheckAndEstimate hits
	// the precise pricing rule for the currently-bound provider+model. Fix for
	// fix/salesrag-pricing-resolve-route — see build-manifest.yaml.
	salesragRegistry := registry.New(b.ds.DB())
	b.salesRAGService = salesrag.NewSalesRAGBizWithCredits(b.ds, pipeline, salesRAGSvc, b.Volc(), b.Ali(), salesSessionStore, parser, creditSvc, pricingCalc, salesragRegistry)

	// 初始化 Agent Tool Registry + AgentRunner（agent-mode #2 + #3 + #4）
	// 顺序敏感：必须在 salesRAGService 之后（PlatformToolFactory 依赖 salesRAGService）
	agentToolRegistry := agent.NewAgentToolRegistry(ds.ToolDefinitions(), ds.ToolFactoryRegistries())

	// V1.5 Track 4 task 4.4: Skill Registry + platform factory construction.
	// Build sandbox pool first so it can be wired into the platform factory before LoadAll.
	// skills_root from viper; if absent or the directory does not exist, the load_skill
	// tool still registers (it always does) but serves only DB-bound skills — the disk
	// platform skills (xlsx/docx/pptx/pdf-author) are unavailable (graceful degradation).
	sandboxLogger := &sandboxZapLogger{}
	sandboxConfig := sandbox.LoadFromViper(viper.GetViper())
	dockerClient := sandbox.NewDockerCLIClient(sandboxLogger)
	sandboxPool := sandbox.NewPool(sandboxConfig, dockerClient, sandboxLogger)
	sandboxHookManager := agent.NewSandboxHookManager(sandboxPool, ds.AgentSandboxSessions())
	agent.SetDefaultHookManager(sandboxHookManager)
	log.Infow("sandbox pool initialized",
		"backend", string(sandboxConfig.Backend),
		"pool_min", sandboxConfig.PoolMin,
		"image_tag", sandboxConfig.ImageTag)

	// 2026-05-29 skill-progressive-loader: build Skill Registry and wire into
	// platform factory. The disk SKILL.md is read by load_skill (open-tools-skill-as-
	// guidance merged read_skill into it) via the registry — it does NOT use the
	// sandbox SkillPool. The agent LLM uses run_python (which has its own sandbox.Pool
	// wiring through the run_python tool) to execute the Python it authors
	// based on the SKILL.md guidance.
	var platformFactory agent.ToolFactory
	var platformSkillReg skills.Registry // captured for WithPlatformSkillRegistry below
	skillsRoot := viper.GetString("sandbox.skills_root")
	if skillsRoot != "" {
		if skillReg, skillRegErr := skills.NewRegistry(skillsRoot); skillRegErr != nil {
			log.Warnw("skills.NewRegistry failed; load_skill will serve DB-bound skills only (no disk platform skills)",
				"skills_root", skillsRoot, "error", skillRegErr)
			platformFactory = agent.NewPlatformToolFactory(b.salesRAGService, ds)
		} else {
			log.Infow("skills.Registry initialised", "skills_root", skillsRoot, "count", len(skillReg.List()))
			// SkillPool type assertion retained for forward compat (run_python
			// still uses sandboxPool); nil-safe even if assertion fails.
			sp, _ := sandboxPool.(sandbox.SkillPool)
			platformFactory = agent.NewPlatformToolFactoryWithSkills(b.salesRAGService, ds, skillReg, sp)
			platformSkillReg = skillReg
			log.Infow("load_skill: disk platform skill registry wired", "count", len(skillReg.List()))
		}
	} else {
		platformFactory = agent.NewPlatformToolFactory(b.salesRAGService, ds)
	}

	// agent-mode-billing T9: wire creditService so image_gen Reserves/Reconciles real credits.
	agent.SetFactoryCreditService(platformFactory, creditSvc)

	if err := agentToolRegistry.RegisterFactory(platformFactory); err != nil {
		log.Warnw("AgentToolRegistry.RegisterFactory failed", "error", err)
	}
	if err := agentToolRegistry.LoadAll(context.Background()); err != nil {
		log.Warnw("AgentToolRegistry.LoadAll failed", "error", err)
	}
	b.agentToolRegistry = agentToolRegistry

	// #6 agent-mode-permission-pipeline: PermissionGate + 7 Validators + WrapHooks
	// 顺序：permission → sandbox（避免 deny 时白启容器；S0 P0 reviewer fix）
	//
	// enforce 开关（remove-permission-backdoor hotfix 2026-06-02）：默认 true=所有环境跑真实 pipeline。
	// SetDefault 兜底使 config_prod.yaml 无需声明该 key（且该文件禁止修改），prod 自动 enforce。
	// 仅显式 agent.permission.enforce=false 时全局 force-allow（高危逃生舱，gate 构造内 loud-warn）。
	// 取代旧 flag.Lookup("test.v") 环境嗅探后门（commit 14754a39）。
	viper.SetDefault("agent.permission.enforce", true)
	enforcePermission := viper.GetBool("agent.permission.enforce")
	b.permissionGate = permission.NewPermissionGate(
		permission.WithStore(ds.AgentPermissions()),
		permission.WithSkillStore(ds.AgentDefinitions()),
		permission.WithEnforce(enforcePermission),
		permission.WithValidators(
			permvalidators.NewPlatformHardRule(),
			permvalidators.NewSandboxOverride(),
			permvalidators.NewTenantAdminRule(ds.AgentPermissions()),
			permvalidators.NewWorkingDir(""),
			permvalidators.NewToolFlag(ds.AgentDefinitions()),
			permvalidators.NewUserSessionRule(),
			// #14/A7 (commit 60b67547): real L3 auto-mode LLM classifier via aiservice.Chat
			// (qwen-turbo, 250ms timeout, fail-allow).
			permvalidators.NewAutoModeLLMValidator(permvalidators.NewAIServiceLLMClassifier()),
		),
	)
	log.Infow("agent permission gate wired", "enforce", enforcePermission)

	// agent-security-hardening: soft-interception config (per-run controller reads this).
	// 命中 permission deny 时只挡这一次工具调用 + 喂回 LLM 续循环（非整 run 终止）；
	// 防呆三阈值。SetDefault 兜底 prod 无需改 config_prod；enabled=false 退回硬终止（高危逃生舱）。
	viper.SetDefault("agent.permission.soft_deny.enabled", true)
	viper.SetDefault("agent.permission.soft_deny.max_same_consecutive", 3)
	viper.SetDefault("agent.permission.soft_deny.max_total_consecutive", 5)
	viper.SetDefault("agent.permission.soft_deny.max_lifetime_per_fingerprint", 10)
	agent.SetSoftDenyConfig(agent.SoftDenyConfig{
		Enabled:     viper.GetBool("agent.permission.soft_deny.enabled"),
		MaxSame:     viper.GetInt("agent.permission.soft_deny.max_same_consecutive"),
		MaxTotal:    viper.GetInt("agent.permission.soft_deny.max_total_consecutive"),
		MaxLifetime: viper.GetInt("agent.permission.soft_deny.max_lifetime_per_fingerprint"),
	})

	// #12 agent-mode-billing-integration: budget tracker + admin_test consumer
	// + BudgetGate hooks 嵌套到 permission 之下，sandbox 之上。
	// Hook chain order: permission(outer) → budget(middle) → sandbox(base)
	//
	// PreToolCall 顺序 permission → budget → sandbox：
	//   - permission 在前：deny 时不暴露预算内部状态（与 #6 P0 reviewer fix 一致）
	//   - budget 在中：permission allow 后再 check 预算
	//   - sandbox 在后：启动容器是最重的副作用，前两层 deny 都不会浪费
	//
	// PostToolCall 自然逆序（外层调内层，内层先返回）：
	//   sandbox 关容器 → budget RecordUsage（拿真实 tokens）→ permission 透传
	budgetTracker := budget.NewTracker(nil) // v1: nil IBudgetStore — daily aggregate 仅 in-process; TODO(#14) Redis
	budgetAdminConsumer := budget.NewAdminTestConsumer(ds)
	budgetGate := budgetgate.NewBudgetGate(budgetTracker, budgetAdminConsumer, ds.AgentRuns())

	// agent-mode-billing T6: a single process-level callID→Usage store shared by
	// the per-run adapters (writers) and budgetgate (reader) so RecordUsage sees
	// real LLM token counts → MaxCredits dimension works.
	callUsageStore := agent.NewCallUsageStore()
	budgetWrappedHooks := budgetGate.WrapHooks(
		sandboxHookManager.AsRunHooks(),
		budgetgate.WithUsageLookup(agent.NewCallUsageLookup(callUsageStore)),
		// budget-tracker-token-units fix: convert tokens→credits before the
		// tracker accumulates — MaxCredits/daily dims are credit-denominated.
		budgetgate.WithPricingCalculator(pricingCalc),
	)
	permWrappedHooks := permission.WrapHooks(budgetWrappedHooks, b.permissionGate)

	// #13 agent-mode-compliance-3layer: construct 3-layer compliance gate.
	// Hook chain order extended: sandbox → budget → permission → compliancegate (outermost)
	// PreToolCall: compliance.CheckToolCall fires first; deny short-circuits before
	// permission/budget/sandbox. Rationale: L0 content violations should not leak
	// internal permission/budget state.
	complianceCache := compliance.NewTTLCache(compliance.DefaultCacheCap, compliance.DefaultCacheTTL)
	complianceTenant := compliance.NewTenantRuleProvider(ds.Compliance(), complianceCache)
	complianceAssembler := compliance.NewSystemPromptAssembler(complianceTenant)
	complianceAudit := compliance.NewAuditLogger(ds.Compliance())
	complianceAudit.Start()
	b.complianceAudit = complianceAudit
	// #14/A6 (commit d66d4656): real LLM injection classifier via aiservice.Chat (qwen-turbo, 300ms timeout, fail-deny).
	complianceInjection := compliance.NewInjectionDetector(compliance.NewAIServiceLLMClassifier())
	b.complianceGate = compliance.NewComplianceGate(complianceAssembler, complianceTenant, complianceInjection, complianceAudit)

	// scope_validator install — GORM Before-Query hook on whitelist agent-mode tables
	complianceScope := compliance.NewScopeValidator(complianceAudit)
	if err := complianceScope.Install(ds.DB()); err != nil {
		log.Warnw("compliance.ScopeValidator.Install failed; scope monitoring disabled", "error", err)
	}

	wrappedHooks := compliancegate.WrapHooks(permWrappedHooks, b.complianceGate)

	// SetAdminTestConsumer 通过 adapter 把 budget.AdminTestConsumer 转 credit.AdminTestConsumer
	// （两个 interface 字段类型签名相同，但 Status 返回类型不同；adapter 做字段转换）。
	b.creditService.(interface {
		SetAdminTestConsumer(credit.AdminTestConsumer)
	}).
		SetAdminTestConsumer(&budgetToCreditAdapter{inner: budgetAdminConsumer})

	// #8 narration-layer: build singleton Provider from configs/tool-display.yaml.
	// S4 amendment to spec S2-D5: NewBiz signature is func(IStore) *biz with no
	// error return, so narration init failure cannot propagate as a hard boot
	// error. Use graceful degrade — log.Errorw + leave narrationProv nil.
	// agent.WithNarrationProvider(nil) is a documented no-op (legacy behavior),
	// so server boots without narration. Operator notices via log and fixes yaml.
	narrationToolNames := agentToolNames(agentToolRegistry)
	narrationProv, narrErr := narration.NewProvider(narration.Config{
		YAMLPath:   "configs/tool-display.yaml",
		BufferSize: 256,
		ToolNames:  narrationToolNames,
		// #14/A5 (commit 44e88acb): real LLM narration via aiservice.Chat (qwen-turbo, 200ms timeout, fail-allow).
		LLMFallback: narration.NewAIServiceLLMFallback(),
	})
	if narrErr != nil {
		log.Errorw("narration provider init failed; narration disabled for this process",
			"error", narrErr,
			"yaml_path", "configs/tool-display.yaml")
		narrationProv = nil
	}

	// #7 memory-system: construct MemoryProvider backed by L1 + L2 stores.
	// #14/A2: wire aiserviceEmbedder for real text-embedding-v4 calls
	// (replaces mockEmbedder zero vectors). v1 mockEmbedder retained for unit tests.
	memoryProvider := memory.NewProvider(
		ds.AgentSessionMemories(),
		ds.UserGlobalMemories(),
		memory.WithEmbedder(memory.NewAIServiceEmbedder()),
	)

	// Task 3.3 (agent-mode-v15-memory-layer-a): construct ExtractorService for
	// async LLM extraction of Layer A user-self facts. Start launches 5 workers
	// that drain a buffered queue and persist confidence≥0.7 facts via aiservice
	// (profile.AgentMemoryExtract → deepseek-v3-2 / qwen-turbo).
	memoryExtractor := memory.NewExtractorService(
		ds.UserMemoryFacts(),
		ds.UserMemoryProfiles(),
	)
	// Use a long-lived background context — Stop() on shutdown drains cleanly.
	memoryExtractor.Start(context.Background())
	// P1.B: stash the service on the biz struct so the shutdown sequence can
	// reach Stop() — mirrors complianceAudit lifecycle (NewBiz constructs +
	// stores; numind.go shutdown calls CloseMemoryExtractor via the biz handle).
	b.memoryExtractor = memoryExtractor

	// Task 3.4 (agent-mode-v15-memory-layer-a): construct SelectorService for
	// per-turn top-5 fact selection. Cache TTL 30s + ≤5-candidate shortcircuit
	// + 3-layer fallback (LLM err / parse err / store err) keep cost ≈ ¥0.001/turn.
	memorySelector := memory.NewSelectorService(ds.UserMemoryFacts())

	// Task 3.6 (agent-mode-v15-memory-layer-a): register explicit defaults for
	// the memory cadence + trivial knobs so config_*.yaml callers see the
	// spec-defined fallback values without each consumer hard-coding them.
	// Spec Step 1 explicit requirement; sibling Load* helpers also fall back
	// gracefully when keys are missing, so the SetDefault is defense-in-depth.
	viper.SetDefault("agent.memory.dialectic_cooldown_seconds", int(memory.DefaultDialecticCooldown/time.Second))
	viper.SetDefault("agent.memory.dialectic_max_cooldown_seconds", int(memory.DefaultDialecticMaxCooldown/time.Second))
	viper.SetDefault("agent.memory.dialectic_min_new_facts", memory.DefaultDialecticMinNewFacts)
	viper.SetDefault("agent.memory.trivial_max_chars", memory.TrivialMaxCharsDefault)

	// Task 3.7 (agent-mode-v15-memory-layer-a): register dialectic LLM-call
	// tunables. Production overrides land in config_local / dev / qa yaml; the
	// LoadDialecticConfigFromViper helper also falls back to the
	// DefaultDialectic* constants when any key is unset.
	viper.SetDefault("agent.memory.dialectic_top_facts_limit", memory.DefaultDialecticTopFactsLimit)
	viper.SetDefault("agent.memory.dialectic_max_output_tokens", memory.DefaultDialecticMaxOutputTokens)
	viper.SetDefault("agent.memory.dialectic_temperature", memory.DefaultDialecticTemperature)
	viper.SetDefault("agent.memory.dialectic_call_timeout_seconds", int(memory.DefaultDialecticCallTimeout/time.Second))

	// Task 3.6 trivial-detector hot-path config wire: load once from viper +
	// stash via SetTrivialConfig so IsTrivial reads through atomic.Value
	// (no viper RWMutex contention on the request path).
	memory.SetTrivialConfig(memory.LoadTrivialConfigFromViper(viper.GetViper()))

	// Task 3.6 (agent-mode-v15-memory-layer-a): construct CadenceService — the
	// dialectic-cadence gate. Read-only over user_memory_profile; consumed by
	// the Task 3.7 dialectic service via b.MemoryCadence() (no AgentRunner wire,
	// since Run-time selector/extractor cost gating uses memory.IsTrivial pure
	// func, not the cadence gate).
	memoryCadence := memory.NewCadenceService(
		ds.UserMemoryProfiles(),
		memory.LoadCadenceConfigFromViper(viper.GetViper()),
	)
	b.memoryCadence = memoryCadence

	// Task 3.7 (agent-mode-v15-memory-layer-a): construct DialecticService —
	// the Layer A "use agent.dialectic LLM to summarise the user themselves"
	// background recompute pipeline. Goroutine-detached + cadence-gated by
	// memoryCadence so per-user cost stays under ¥0.05/day. cached_insight
	// is read synchronously at user-turn start via GetCachedInsight (no LLM
	// call on the request hot path).
	//
	// Wire ordering: dialectic needs the same stores as cadence + the cadence
	// service itself; constructed after memoryCadence and BEFORE the
	// ExtractorService.SetDialecticService hookup below.
	memoryDialectic := memory.NewDialecticService(
		ds.UserMemoryFacts(),
		ds.UserMemoryProfiles(),
		memoryCadence,
		memory.LoadDialecticConfigFromViper(viper.GetViper()),
	)
	b.memoryDialectic = memoryDialectic

	// Hook the dialectic recompute into the ExtractorService: every successful
	// persistFacts will fire MaybeRecompute(userID) (cadence-gated, non-
	// blocking). This is a setter (not a constructor argument) because
	// ExtractorService is constructed earlier in the wire order — the dialectic
	// service depends on memoryCadence which itself depends on profile-store
	// readiness via biz init; cleanest decoupling is the SetDialecticService
	// post-wire.
	memoryExtractor.SetDialecticService(memoryDialectic)

	// Task 3.8 (agent-mode-v15-memory-layer-a): construct TemporalService +
	// DigestGenerator + DigestCron + CronRunner — the 分层时间感知 ("temporal
	// tree") stack. 4 cron jobs (daily/weekly/monthly/quarterly) run on
	// Asia/Shanghai schedules; per-turn injection scans user input for time
	// keywords and pulls the matching digest into the system prompt.
	//
	// Lifecycle: CronRunner.Start spawns the scheduler; shutdown via
	// CloseDigestCron (b.memoryDigestCron.Stop) drains in-flight jobs.
	//
	// Layer A only: all digests are per-user, never aggregated across
	// parent/child accounts (D7 isolation).

	// Register digest-related viper defaults so any caller path
	// (LoadDigestCronConfigFromViper etc.) sees the spec-defined values
	// even when the yaml is silent.
	viper.SetDefault("agent.memory.digest.enabled", true)
	viper.SetDefault("agent.memory.digest.timezone", memory.ShanghaiTZ)
	viper.SetDefault("agent.memory.digest.daily_cron", memory.DefaultDailyCron)
	viper.SetDefault("agent.memory.digest.weekly_cron", memory.DefaultWeeklyCron)
	viper.SetDefault("agent.memory.digest.monthly_cron", memory.DefaultMonthlyCron)
	viper.SetDefault("agent.memory.digest.quarterly_cron", memory.DefaultQuarterlyCron)
	viper.SetDefault("agent.memory.digest.worker_concurrency", memory.DefaultDigestWorkerConcurrency)
	viper.SetDefault("agent.memory.digest.redis_lock_ttl_seconds", int(memory.DefaultDigestLockTTL/time.Second))
	viper.SetDefault("agent.memory.digest.per_user_timeout_seconds", int(memory.DefaultDigestPerUserTimeout/time.Second))
	viper.SetDefault("agent.memory.digest.max_output_tokens", memory.DefaultDigestMaxOutputTokens)
	viper.SetDefault("agent.memory.digest.temperature", memory.DefaultDigestTemperature)
	viper.SetDefault("agent.memory.digest.call_timeout_seconds", int(memory.DefaultDigestCallTimeout/time.Second))

	// TemporalService: per-turn injector (read-only, no LLM).
	memoryTemporal := memory.NewTemporalService(ds.UserMemoryDigests())
	b.memoryTemporal = memoryTemporal

	// DigestGenerator: pulls source data (agent_run / lower digests) + calls
	// aiservice.Chat(profile.AgentDigest) with 1 retry + fallback.
	digestGen := memory.NewDigestGenerator(
		ds.UserMemoryDigests(),
		ds.UserMemoryFacts(),
		memory.LoadDigestConfigFromViper(viper.GetViper()),
	)

	// DigestCron: enumerates active users + worker pool + per-user generate +
	// upsert + Redis SETNX lock (rdb may be nil = single-instance mode).
	digestCronOpts := []memory.DigestCronOption{}
	if rdb := redisPkgClient(); rdb != nil {
		// Wrap *redis.Client in a typed accessor — passing a typed-nil
		// *redis.Client directly through the interface seam confuses the
		// downstream nil-check (typed nil != nil interface).
		digestCronOpts = append(digestCronOpts, memory.WithDigestCronRedisClient(rdb))
	}
	digestCron := memory.NewDigestCron(
		ds.UserMemoryDigests(),
		digestGen,
		memory.LoadDigestCronConfigFromViper(viper.GetViper()),
		digestCronOpts...,
	)

	// CronRunner: robfig/cron scheduler with the 4 jobs (daily / weekly /
	// monthly / quarterly) — Asia/Shanghai timezone, spec cron exprs.
	cronCfg := memory.LoadCronRunnerConfigFromViper(viper.GetViper())
	digestRunner := memory.NewCronRunner(digestCron, cronCfg)
	if err := digestRunner.Start(); err != nil {
		log.Errorw("memory.digest CronRunner.Start failed; cron disabled for this process",
			"error", err)
	}
	b.memoryDigestCron = digestRunner

	// Task 3.5 (agent-mode-v15-memory-layer-a): construct FULLTEXT search
	// service. Indexes diff-by-uuid on every AgentRunner.WriteTurn via the
	// failure-tolerant IndexAgentRun hook (search rows are derived data —
	// errors log warn and never block the run). BackfillFromAgentRun (CLI +
	// repair) needs the AgentRuns store handle to fetch source messages.
	searchService := search.NewService(
		ds.AgentMessageSearches(),
		ds.AgentRuns(),
	)
	b.searchService = searchService

	// V1.5 板块 2 task 2.2 — V2 路径 artifact deps：写盘目录 + ArtifactStore。
	// 仅当 agent_run.use_compact_v2=true 时生效（runner.go 内有 gate）；
	// 默认 false → 行为与 V1.5 之前完全一致。
	artifactDir := viper.GetString("agent.artifact_dir")
	if artifactDir == "" {
		artifactDir = "data"
		log.Warnw("biz.NewBiz: agent.artifact_dir not configured; using relative ./data — set in config_*.yaml for prod")
	}

	// v2 #2 agent-mode-v2-skill-invocation: BindingService 让 runner.Run 启动时
	// 通过 ListByAgent (已 join sort_order asc) 拿到 Agent 装载的 Skill 列表。
	// nil 时 runner 走 legacy 路径 (dual-read 兜底，v1 Agent 行为零回归)。
	skillBindingSvc := skillartifact.NewBindingService(ds.DB())

	b.agentRunner = agent.NewAgentRunner(
		ds.AgentRuns(),
		agentToolRegistry,
		agent.WithDefaultHooks(wrappedHooks),              // #6: permission → sandbox chain
		agent.WithSkillStore(ds.AgentDefinitions()),       // #5 skill-system
		agent.WithUserStore(ds.Users()),                   // b2b2c-student-agent-access: resolve caller parent_user_id for tenant access
		agent.WithSkillBindingService(skillBindingSvc),    // v2 #2 agent-mode-v2-skill-invocation
		agent.WithPlatformSkillRegistry(platformSkillReg), // 2026-05-29 skill-progressive-loader (nil if skills_root unset)
		// V1.5 compact-v1-removal — WithCompactProvider/WithCompactConfig removed.
		// V2 (compactv2) now handles all context-window management; legacy V1
		// recovery helpers (PTL chain + max_output escalation) were removed
		// because their prevention-first replacement covers the same cases.
		agent.WithNarrationProvider(narrationProv), // #8 narration-layer (nil if init failed)
		agent.WithMemoryProvider(memoryProvider),   // #7 memory-system
		agent.WithBudgetTracker(budgetTracker),     // #12 agent-mode-billing-integration
		agent.WithCallUsageStore(callUsageStore),   // agent-mode-billing T6: shared usage store (MaxCredits)
		agent.WithComplianceGate(b.complianceGate), // #13 agent-mode-compliance-3layer
		agent.WithMemoryExtractor(memoryExtractor), // Task 3.3 LLM extraction async pipeline
		agent.WithMemorySelector(memorySelector),   // Task 3.4 top-5 side-query selector
		agent.WithMemoryDialectic(memoryDialectic), // Task 3.7 Layer A cached_insight injection
		agent.WithMemoryTemporal(memoryTemporal),   // Task 3.8 temporal digest injection (4 granularities)
		agent.WithSearchService(searchService),     // Task 3.5 FULLTEXT ngram indexing hook
		// V1.5 v2-compact-adapter-integration — V2 L0 工具写盘 deps（adapter compactor
		// 自管 prevention 状态，不再需要 IAgentCompactV2Store）。
		agent.WithCompactV2Deps(ds.ToolArtifact(), artifactDir),
	)

	// 初始化知识库服务
	b.kbService = kbbiz.NewKnowledgeBaseBiz(ds, b.salesRAGService)

	// 初始化智能体服务。LLM 调用统一走 aiservice Gateway（Task 9 起）。
	// T2.1：chatbot 改走底座检索（query 改写 + 多路检索 + rerank + grounding）修"回答怪"。
	// chatMode="free" 避免销售话术污染纯知识库问答的 query 改写；docStore=nil 因为 chatbot
	// 始终用显式 docIDs scope（不需要 AllEnabled 解析）。只有挂了 KB 且解析出 docIDs 才调它。
	chatbotRetrieve := retrieve.NewService(vStore, salesragservice.NewRouterRewriter(llmRouter, "free"), nil)
	b.chatbotService = chatbotbiz.NewChatbotBiz(ds, chatbotRetrieve)

	// 初始化博主监控服务
	monitorCooldown := monitor.NewCooldownManager(
		viper.GetInt("monitor.cooldown.check_minutes"),
		viper.GetInt("monitor.cooldown.analyze_minutes"),
	)
	b.monitorService = monitor.NewMonitorBiz(ds, monitorCooldown)

	// 系统内置观点赛道初始化（异步，不阻塞启动，5 分钟超时保护）
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorw("Panic in opinion track seeder", "recover", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		seeder := seed.NewSeeder(b.ds.DB())
		results := seeder.SeedOpinionTracks(ctx)
		for slug, result := range results {
			log.Infow("Seeding opinion track", "slug", slug, "opinions", result.Count)
			docID, err := b.salesRAGService.Ingest(ctx, 0, slug+".md", result.Track.Name+" - 系统观点库", result.MarkdownReader(), salesrag.IngestOptions{
				Description: result.Track.Desc,
				Tags:        []string{"观点库", "系统内置", slug},
			})
			if err != nil {
				log.Errorw("Failed to ingest opinion track", "slug", slug, "error", err)
				continue
			}
			// 标记为系统文档
			if err := b.ds.KnowledgeDocuments().UpdateColumns(ctx, docID, map[string]interface{}{"is_system": true}); err != nil {
				log.Errorw("Failed to mark document as system", "slug", slug, "doc_id", docID, "error", err)
			}
			// 创建/更新赛道记录
			if err := seeder.CreateOrUpdateTrack(ctx, slug, docID); err != nil {
				log.Errorw("Failed to create opinion track record", "slug", slug, "error", err)
			}
			log.Infow("Opinion track seeded", "slug", slug, "doc_id", docID)
		}
	}()

	// #14 follow-up ALPHA: student-facing agent query service.
	// WithSkillStore → agent_name/emoji/credit_cap enrichment.
	// WithCreditStore → credits_used computation via SumByReservationIDs.
	b.studentQuerySvc = agent.NewStudentQueryService(
		ds.AgentRuns(),
		ds.Users(),
		agent.WithQuerySkillStore(ds.AgentDefinitions()),
		agent.WithQueryCreditStore(ds.Credits()),
	)

	// #14 BETA: student-facing run lifecycle service.
	// narrationProv is wired alongside the buffer so StudentRunService can
	// bridge Provider.Emit → Buffer per run (without it PollNarration returns
	// [] forever and the learner UI sees no tool-call narration even when the
	// run works end-to-end). narrationProv may be nil if YAML init failed
	// earlier in this function; the bridge handles that gracefully.
	narrationBuf := agent.NewNarrationBuffer(256, 30*time.Minute)
	b.studentRunSvc = agent.NewStudentRunService(
		b.agentRunner,
		ds.AgentRuns(),
		ds.AgentDefinitions(),
		pricingCalc,
		narrationProv,
		narrationBuf,
	).WithUserStore(ds.Users()) // b2b2c-student-agent-access: resolve caller parent_user_id for tenant access

	// V1.5 task 1.2: wire attachment fallback service + upload service with fallback.
	b.attachFallbackSvc = agentatt.NewFallbackService(ds.AgentAttachments())
	b.uploadSvc = attachment.NewUploadServiceWithFallback(ds.AgentAttachments(), b.attachFallbackSvc)

	// 设置全局单例，供 middleware/cron 等无法注入 biz 的代码路径使用。
	// 确保 store.S 已在 numind.go 中完成初始化后才调用 NewBiz。
	B = b

	return b
}

// Users 返回一个实现了 UserBiz 接口的实例.
func (b *biz) Users() user.UserBiz {
	return user.New(b.ds)
}

// Ali 返回一个实现了 AliBiz 接口的实例.
func (b *biz) Ali() ali.AliBiz {
	return ali.NewAliBiz(b.ds)
}

// Volc 返回一个实现了 VolcBiz 接口的实例.
func (b *biz) Volc() volc.VolcBiz {
	return volc.NewVolcBiz(b.ds)
}

// Configs 返回一个实现了 ConfigBiz 接口的实例.
func (b *biz) Configs() config.ConfigBiz {
	return config.New(b.ds)
}

// Sop 返回 SOP 服务实例.
func (b *biz) Sop() sopbiz.ISopBiz {
	return b.sopService
}

// Customers 返回客户管理服务实例.
func (b *biz) Customers() customerbiz.ICustomerBiz {
	return customerbiz.New(b.ds)
}

// SalesRAG 返回销售 RAG 服务实例.
func (b *biz) SalesRAG() salesrag.SalesRAGBiz {
	return b.salesRAGService
}

// Credit 返回积分服务实例.
func (b *biz) Credit() credit.ICreditBiz {
	return b.credit
}

// CreditService 返回 credits-system ICreditService 统一入口.
func (b *biz) CreditService() credit.ICreditService {
	return b.creditService
}

// Pricing 返回 pricing.ICalculator 实例（同步成本计算）.
func (b *biz) Pricing() pricing.ICalculator {
	return b.pricing
}

// Payment 返回支付服务实例.
func (b *biz) Payment() payment.IPaymentBiz {
	return b.payment
}

// Monitor 返回博主监控服务实例.
func (b *biz) Monitor() monitor.IMonitorBiz {
	return b.monitorService
}

// KnowledgeBase 返回知识库服务实例.
func (b *biz) KnowledgeBase() kbbiz.IKnowledgeBaseBiz {
	return b.kbService
}

// Chatbot 返回智能体服务实例.
func (b *biz) Chatbot() chatbotbiz.IChatbotBiz {
	return b.chatbotService
}

// Agents 返回 Agent Runtime 实例（agent-mode #2 runtime-skeleton）。
func (b *biz) Agents() agent.AgentRunner {
	return b.agentRunner
}

// AgentTools 返回 Agent Tool Registry 实例（agent-mode #3 tool-registry）。
func (b *biz) AgentTools() agent.AgentToolRegistry {
	return b.agentToolRegistry
}

// Skill 返回 Agent Skill 服务实例（#5/14 skill-system）。
func (b *biz) Skill() skillbiz.Service {
	return skillbiz.NewService(b.ds)
}

// StudentQuery 返回学生端 agent 查询服务实例（#14 follow-up ALPHA）。
func (b *biz) StudentQuery() *agent.StudentQueryService {
	return b.studentQuerySvc
}

// StudentRun 返回学生端 run lifecycle 服务实例（#14 BETA）。
func (b *biz) StudentRun() *agent.StudentRunService {
	return b.studentRunSvc
}

// Attachment 返回文件上传服务实例（#14 BETA → V1.5 task 1.2 升级带 fallback）。
func (b *biz) Attachment() *attachment.UploadService {
	if b.uploadSvc != nil {
		return b.uploadSvc
	}
	return attachment.NewUploadService()
}

// MemoryCadence 返回 Task 3.6 dialectic cadence gate 实例。
// Task 3.7 dialectic service 通过本 getter 在每轮 dialectic 决策前问
// ShouldRunDialectic，决定跑实际 LLM 调用还是用缓存 insight。
// agent-mode-v15-memory-layer-a Task 3.6.
func (b *biz) MemoryCadence() *memory.CadenceService {
	return b.memoryCadence
}

// SearchService 返回 Task 3.5 FULLTEXT 搜索服务实例。
// router.go 通过本 getter 拿到 search service 实例注册
// GET /v1/agent-runs/search 路由；同 service 已通过 WithSearchService 注入
// AgentRunner 用于 WriteTurn 后的 IndexAgentRun 钩子。
// agent-mode-v15-memory-layer-a Task 3.5.
func (b *biz) SearchService() search.Service {
	return b.searchService
}

// AttachmentFallback 返回异步 fallback 生成服务实例（V1.5 task 1.2）。
func (b *biz) AttachmentFallback() agentatt.FallbackService {
	return b.attachFallbackSvc
}

// PermissionGate 返回 Permission 网关实例（agent-mode #6 permission-pipeline）。
// 暴露用于 shutdown sequence（main.go 调 ClosePermissionGate）+ 测试 introspect。
func (b *biz) PermissionGate() *permission.PermissionGate {
	return b.permissionGate
}

// ClosePermissionGate 优雅停止 permission audit goroutine（5s 内 drain）。
// main.go shutdown sequence 应在 server.Shutdown 后调；如未调，进程退出时 drainer
// goroutine 随之退出（无泄漏，仅丢失 in-flight 审计；trade-off 已文档化）。
func (b *biz) ClosePermissionGate() {
	if b.permissionGate != nil {
		b.permissionGate.Close()
	}
}

// CloseComplianceAudit 优雅停止 compliance audit logger consumer goroutine。
// 与 ClosePermissionGate 配对调用；进程退出时若未调，consumer 随进程结束（无内存
// 泄漏，仅丢失 in-flight 审计 entries —— 与 #6 同模式 trade-off）。
// #13 agent-mode-compliance-3layer.
func (b *biz) CloseComplianceAudit(ctx context.Context) {
	if b.complianceAudit != nil {
		if err := b.complianceAudit.Stop(ctx); err != nil {
			log.Warnw("compliance audit logger stop timed out",
				"error", err, "drop_count", b.complianceAudit.DropCount())
		}
	}
}

// CloseMemoryExtractor 优雅停止 Task 3.3 ExtractorService 的 5 worker goroutine
// （context cancel + close queue + wg.Wait）。
// 与 ClosePermissionGate / CloseComplianceAudit 同 shutdown 模式：未调时进程退出
// goroutine 随之结束，但 in-flight LLM 调用可能被中断（无 DB 一致性风险 —— 失败
// 的 extract 不重试，下一轮 turn 自然再触发）。Stop 是 idempotent。
// Stop 内部已处理 ctx — 此方法接受 ctx 参数仅为接口对齐 CloseComplianceAudit 风格。
// agent-mode-v15-memory-layer-a Task 3.3.
func (b *biz) CloseMemoryExtractor(_ context.Context) {
	if b.memoryExtractor != nil {
		b.memoryExtractor.Stop()
	}
}

// CloseDigestCron 优雅停止 Task 3.8 memory digest 的 4 个 cron job
// (daily/weekly/monthly/quarterly). Stop 等待 in-flight job 完成 (worker pool
// drain + Redis lock release best-effort). Idempotent.
// 与 CloseMemoryExtractor 同 shutdown 模式: 未调时进程退出 robfig/cron 随之结束;
// in-flight LLM 调用可能被中断 (无 DB 一致性风险 — UPSERT 幂等, 下次 cron 重跑覆盖).
// agent-mode-v15-memory-layer-a Task 3.8.
func (b *biz) CloseDigestCron(_ context.Context) {
	if b.memoryDigestCron != nil {
		b.memoryDigestCron.Stop()
	}
}

// redisPkgClient returns the package-level *redis.Client, or nil if Redis was
// not initialised (e.g. tests, single-instance dev without redis). The
// memory.WithDigestCronRedisClient option accepts the minimal interface
// (SetNX + Del); *redis.Client structurally satisfies it.
//
// nil = "no lock" mode — DigestCron will skip the Redis SETNX gate and run
// every scheduled tick. Safe for single-instance deploys.
func redisPkgClient() *goredis.Client {
	return redispkg.GetClient()
}

// LLMRouter 返回 LLM 路由服务实例.
func (b *biz) LLMRouter() *llmrouter.Router {
	return b.llmRouterSvc
}

// sandboxZapLogger adapts the numind log package to sandbox.Logger interface.
// Defined in this file (not in pkg/log) to keep sandbox decoupled from zap.
type sandboxZapLogger struct{}

func (sandboxZapLogger) Warnw(msg string, kv ...interface{}) { log.Warnw(msg, kv...) }
func (sandboxZapLogger) Infow(msg string, kv ...interface{}) { log.Infow(msg, kv...) }

// agentToolNames extracts FullTool names from the registry for narration's
// boot-time yaml-key validation (#8). Best-effort: if the registry has not
// loaded tools yet (lazy load timing), returns an empty slice and narration
// skips the missing-key warn — acceptable per S2-D4.
func agentToolNames(reg agent.AgentToolRegistry) []string {
	if reg == nil {
		return nil
	}
	tools := reg.ListAllTools()
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}

// budgetToCreditAdapter adapts budget.AdminTestConsumer to credit.AdminTestConsumer.
// Required because the import-cycle decoupling (M10 decision) keeps Consume/Refund
// method signatures identical but uses two separate AdminTestStatus types (one in
// each package). This adapter does field-by-field conversion at the boundary.
// #12 agent-mode-billing-integration.
type budgetToCreditAdapter struct {
	inner budget.AdminTestConsumer
}

func (a *budgetToCreditAdapter) Consume(ctx context.Context, parentUserID uint, amount int64) (uint64, error) {
	return a.inner.Consume(ctx, parentUserID, amount)
}

func (a *budgetToCreditAdapter) Refund(ctx context.Context, parentUserID uint, txID uint64, refundAmount int64) error {
	return a.inner.Refund(ctx, parentUserID, txID, refundAmount)
}

func (a *budgetToCreditAdapter) Status(ctx context.Context, parentUserID uint, now time.Time) (*credit.AdminTestStatus, error) {
	bs, err := a.inner.Status(ctx, parentUserID, now)
	if err != nil {
		return nil, err
	}
	if bs == nil {
		return nil, nil
	}
	return &credit.AdminTestStatus{
		Granted:      bs.Granted,
		Used:         bs.Used,
		Remaining:    bs.Remaining,
		PeriodStart:  bs.PeriodStart,
		PeriodEnd:    bs.PeriodEnd,
		DaysToExpire: bs.DaysToExpire,
	}, nil
}
