package biz

//go:generate mockgen -destination mock_biz.go -package biz github.com/marmotedu/miniblog/internal/miniblog/biz IBiz

import (
	"context"
	"path/filepath"
	"time"

	"numind-server/internal/numind/biz/ali"
	chatbotbiz "numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/biz/config"
	"numind-server/internal/numind/biz/credit"
	customerbiz "numind-server/internal/numind/biz/customer"
	kbbiz "numind-server/internal/numind/biz/knowledgebase"
	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/numind/biz/monitor"
	"numind-server/internal/numind/biz/payment"
	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/numind/biz/salesrag/seed"
	salesragservice "numind-server/internal/numind/biz/salesrag/service"
	sopbiz "numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/llm"
	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// IBiz 定义了 Biz 层需要实现的方法.
type IBiz interface {
	Users() user.UserBiz
	Ali() ali.AliBiz
	Volc() volc.VolcBiz
	Configs() config.ConfigBiz
	Sop() sopbiz.ISopBiz                 // SOP服务
	Customers() customerbiz.ICustomerBiz // 客户管理服务
	SalesRAG() salesrag.SalesRAGBiz      // 销售 RAG 服务
	Credit() credit.ICreditBiz           // 积分服务
	Payment() payment.IPaymentBiz              // 支付服务
	Monitor() monitor.IMonitorBiz              // 博主监控服务
	KnowledgeBase() kbbiz.IKnowledgeBaseBiz    // 知识库服务
	Chatbot() chatbotbiz.IChatbotBiz           // 智能体服务
	LLMRouter() *llmrouter.Router              // LLM 路由服务
}

// 确保 biz 实现了 IBiz 接口.
var _ IBiz = (*biz)(nil)

// biz 是 IBiz 的一个具体实现.
type biz struct {
	ds              store.IStore
	sopService      sopbiz.ISopBiz
	salesRAGService salesrag.SalesRAGBiz
	credit          credit.ICreditBiz
	payment         payment.IPaymentBiz
	monitorService  monitor.IMonitorBiz
	kbService       kbbiz.IKnowledgeBaseBiz
	chatbotService  chatbotbiz.IChatbotBiz
	llmRouterSvc    *llmrouter.Router
}

// 确保 biz 实现了 IBiz 接口.
var _ IBiz = (*biz)(nil)

// NewBiz 创建一个 IBiz 类型的实例.
func NewBiz(ds store.IStore) *biz {
	creditBiz := credit.NewCreditBiz(ds)
	b := &biz{
		ds:           ds,
		credit:       creditBiz,
		payment:      payment.NewPaymentBiz(ds, creditBiz),
		llmRouterSvc: llmrouter.New(ds),
	}

	// 创建 ConfigReader，用于从 Redis → MySQL → Viper 读取配置
	_ = config.NewConfigReader(b.Configs())

	// 初始化SOP服务（注入 LLMRouter 以支持用户选择模型）
	sopExecutor := sopbiz.NewSopExecutor(b.ds, b.llmRouterSvc)
	b.sopService = sopbiz.NewSopBiz(b.ds, sopExecutor, b.credit)

	// 初始化销售 RAG 服务
	// 向量库支持 sqlitevec（默认）、dashvector（回退兼容）、memory（测试）
	var vStore port.VectorStore

	// Define Embedder using Qwen
	embedder := func(ctx context.Context, text string) ([]float32, error) {
		vec, _, err := b.Ali().QianwenEmbedding(ctx, text)
		return vec, err
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
	parser := adapter.NewEnhancedParser()
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

	b.salesRAGService = salesrag.NewSalesRAGBiz(b.ds, pipeline, salesRAGSvc, b.Volc(), b.Ali(), salesSessionStore, parser)

	// 初始化知识库服务
	b.kbService = kbbiz.NewKnowledgeBaseBiz(ds, b.salesRAGService)

	// 初始化智能体服务（注入 LLMRouter + VectorStore + Embedder 用于 ChatStream）
	b.chatbotService = chatbotbiz.NewChatbotBiz(ds, b.llmRouterSvc, vStore, embedder)

	// 初始化博主监控服务
	llmClient := llm.NewDMXAPIClient()
	monitorCooldown := monitor.NewCooldownManager(
		viper.GetInt("monitor.cooldown.check_minutes"),
		viper.GetInt("monitor.cooldown.analyze_minutes"),
	)
	b.monitorService = monitor.NewMonitorBiz(ds, llmClient, monitorCooldown)

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

// LLMRouter 返回 LLM 路由服务实例.
func (b *biz) LLMRouter() *llmrouter.Router {
	return b.llmRouterSvc
}
