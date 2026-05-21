package biz

//go:generate mockgen -destination mock_biz.go -package biz github.com/marmotedu/miniblog/internal/miniblog/biz IBiz

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/budgetgate"
	"numind-server/internal/numind/biz/agent/compliancegate"
	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/budget"
	chatbotbiz "numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/biz/compact"
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
	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/numind/biz/salesrag/seed"
	salesragservice "numind-server/internal/numind/biz/salesrag/service"
	"numind-server/internal/numind/biz/sandbox"
	sopbiz "numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/log"
	docparser "numind-server/internal/pkg/parser"
	"numind-server/internal/pkg/pricing"

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
	Sop() sopbiz.ISopBiz                    // SOP服务
	Customers() customerbiz.ICustomerBiz    // 客户管理服务
	SalesRAG() salesrag.SalesRAGBiz         // 销售 RAG 服务
	Credit() credit.ICreditBiz              // 积分服务
	CreditService() credit.ICreditService   // credits-system ICreditService 统一入口
	Pricing() pricing.ICalculator           // pricing 同步成本计算
	Payment() payment.IPaymentBiz           // 支付服务
	Monitor() monitor.IMonitorBiz           // 博主监控服务
	KnowledgeBase() kbbiz.IKnowledgeBaseBiz // 知识库服务
	Chatbot() chatbotbiz.IChatbotBiz        // 智能体服务
	LLMRouter() *llmrouter.Router           // LLM 路由服务
	Agents() agent.AgentRunner              // Agent Runtime（agent-mode #2）
	AgentTools() agent.AgentToolRegistry    // Agent Tool Registry（agent-mode #3）
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
	embedder := func(ctx context.Context, text string) ([]float32, error) {
		resp, err := aiservice.Embed(ctx, profile.SalesragEmbed, aiservice.EmbedRequest{
			Texts:     []string{text},
			Dimension: 2048,
		})
		if err != nil {
			return nil, err
		}
		if len(resp.Embeddings) == 0 {
			return nil, fmt.Errorf("salesrag embed: empty embedding response")
		}
		return resp.Embeddings[0], nil
	}

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
		vStore, vecErr = adapter.NewSQLiteVecStore(vecDBPath, embedder)
		if vecErr != nil {
			log.Errorw("Failed to initialize SQLiteVecStore, falling back to MemoryStore", "error", vecErr, "path", vecDBPath)
			vStore = adapter.NewMemoryStore()
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
		vStore = adapter.NewDashVectorStore(dashEndpoint, dashApiKey, dashCollection, embedder)
		log.Infow("Initialized DashVector store", "endpoint", dashEndpoint, "collection", dashCollection)
	case "memory":
		vStore = adapter.NewMemoryStore()
		log.Infow("Initialized MemoryStore (testing only)")
	default:
		log.Warnw("Unknown vector store type, falling back to MemoryStore", "type", vectorStoreType)
		vStore = adapter.NewMemoryStore()
	}

	// 初始化 LLM 意图路由器（V2: 使用 DMXAPI qwen-turbo-latest）
	llmRouter := adapter.NewLLMRouter()

	// Initialize Pipeline Components
	parser := docparser.NewDocumentParser()
	// 使用增强版切分器（支持中文分词、语义边界、100字符重叠、Markdown分级）
	splitter := salesragservice.NewCompatibilitySplitter(salesragservice.SplitterConfig{
		MaxChunkSize: 1000,
		MinChunkSize: 200,
	})
	tagger := salesragservice.NewContentTagger()

	// Initialize Ingestion Pipeline (托管模式下不需要传 embedder)
	pipeline := salesragservice.NewIngestionPipeline(parser, splitter, tagger, b.ds.KnowledgeDocuments(), vStore, b.ds.KnowledgeChunks())

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
	if err := agentToolRegistry.RegisterFactory(agent.NewPlatformToolFactory(b.salesRAGService, ds)); err != nil {
		log.Warnw("AgentToolRegistry.RegisterFactory failed", "error", err)
	}
	if err := agentToolRegistry.LoadAll(context.Background()); err != nil {
		log.Warnw("AgentToolRegistry.LoadAll failed", "error", err)
	}
	b.agentToolRegistry = agentToolRegistry

	// #4 sandbox-integration: SandboxConfig from viper + DockerClient + Pool +
	// SandboxHookManager wire. Default backend (no `sandbox:` config section)
	// is BackendDisabled → Pool is a no-op disabledPool → prod safe.
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

	// #6 agent-mode-permission-pipeline: PermissionGate + 7 Validators + WrapHooks
	// 顺序：permission → sandbox（避免 deny 时白启容器；S0 P0 reviewer fix）
	b.permissionGate = permission.NewPermissionGate(
		permission.WithStore(ds.AgentPermissions()),
		permission.WithSkillStore(ds.AgentDefinitions()),
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

	budgetWrappedHooks := budgetGate.WrapHooks(sandboxHookManager.AsRunHooks())
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

	b.agentRunner = agent.NewAgentRunner(
		ds.AgentRuns(),
		agentToolRegistry,
		agent.WithDefaultHooks(wrappedHooks),        // #6: permission → sandbox chain
		agent.WithSkillStore(ds.AgentDefinitions()), // #5 skill-system
		// #14/A4 (commit 5035d4b7): real LLM compact via aiservice.Chat.
		agent.WithCompactProvider(compact.NewAIServiceCompactProvider(compact.DefaultConfig())),
		// WithCompactConfig omitted — DefaultConfig (qwen-plus) applies.
		agent.WithNarrationProvider(narrationProv), // #8 narration-layer (nil if init failed)
		agent.WithMemoryProvider(memoryProvider),   // #7 memory-system
		agent.WithBudgetTracker(budgetTracker),     // #12 agent-mode-billing-integration
		agent.WithComplianceGate(b.complianceGate), // #13 agent-mode-compliance-3layer
	)

	// 初始化知识库服务
	b.kbService = kbbiz.NewKnowledgeBaseBiz(ds, b.salesRAGService)

	// 初始化智能体服务。LLM 调用统一走 aiservice Gateway（Task 9 起），
	// LLMRouter 参数已移除；此处仅需 VectorStore + Embedder。
	b.chatbotService = chatbotbiz.NewChatbotBiz(ds, vStore, embedder)

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
