package biz

//go:generate mockgen -destination mock_biz.go -package biz github.com/marmotedu/miniblog/internal/miniblog/biz IBiz

import (
	"context"
	"path/filepath"

	accountrecordbiz "numind-server/internal/numind/biz/account_record"
	"numind-server/internal/numind/biz/admin"
	adminaccountbiz "numind-server/internal/numind/biz/admin_account"
	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/article"
	"numind-server/internal/numind/biz/baidu"
	"numind-server/internal/numind/biz/book"
	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/category"
	"numind-server/internal/numind/biz/chat"
	"numind-server/internal/numind/biz/config"
	customerbiz "numind-server/internal/numind/biz/customer"
	"numind-server/internal/numind/biz/feedback"
	"numind-server/internal/numind/biz/image"
	"numind-server/internal/numind/biz/order"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/numind/biz/payment"
	ragbiz "numind-server/internal/numind/biz/rag"
	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/port"
	salesragservice "numind-server/internal/numind/biz/salesrag/service"
	sopbiz "numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/biz/template"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/biz/wechat"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// IBiz 定义了 Biz 层需要实现的方法.
type IBiz interface {
	Users() user.UserBiz
	Images() image.ImageBiz
	Cards() card.CardBiz
	Books() book.BookBiz
	Categories() category.CategoryBiz
	Templates() template.TemplateBiz
	Feedbacks() feedback.FeedbackBiz
	Baidu() baidu.BaiduBiz
	Wechat() wechat.WechatBiz
	Ali() ali.AliBiz
	Volc() volc.VolcBiz
	Pagination() pagination.PaginationBiz
	Chats() chat.ChatBiz
	Article() article.IArticleBiz
	Admin() admin.IAdminBiz
	AdminAccounts() adminaccountbiz.AdminAccountBiz
	Configs() config.ConfigBiz
	Payments() payment.PaymentBiz
	AccountRecords() accountrecordbiz.AccountRecordBiz
	Rag() *ragbiz.RagService             // RAG服务
	Sop() sopbiz.ISopBiz                 // SOP服务
	Customers() customerbiz.ICustomerBiz // 客户管理服务
	SalesRAG() salesrag.SalesRAGBiz      // 销售 RAG 服务
}

// 确保 biz 实现了 IBiz 接口.
var _ IBiz = (*biz)(nil)

// biz 是 IBiz 的一个具体实现.
type biz struct {
	ds              store.IStore
	ragService      *ragbiz.RagService
	sopService      sopbiz.ISopBiz
	salesRAGService salesrag.SalesRAGBiz
}

// 确保 biz 实现了 IBiz 接口.
var _ IBiz = (*biz)(nil)

// NewBiz 创建一个 IBiz 类型的实例.
func NewBiz(ds store.IStore) *biz {
	b := &biz{ds: ds}

	// 初始化 RAG 服务（向量数据库路径）
	// 优先级：rag.vector_db_path 配置 > 基于 resource.image_path 计算 > 默认路径
	dbPath := viper.GetString("rag.vector_db_path")
	if dbPath == "" {
		// 如果没有配置 rag.vector_db_path，则基于 resource.image_path 计算路径
		imagePath := viper.GetString("resource.image_path")
		if imagePath != "" {
			// 从 image_path 获取父目录，在同级创建 vector_db 目录
			// 例如1：/opt/numind/dev/image/upload -> /opt/numind/dev/vector_db
			// 例如2：/Users/.../res/upload -> /Users/.../res/vector_db
			// 判断路径中是否包含 "image" 目录
			parentDir := filepath.Dir(imagePath) // 移除 upload
			if filepath.Base(parentDir) == "image" {
				// 如果父目录是 image，则再向上一级
				// /opt/numind/dev/image -> /opt/numind/dev
				baseDir := filepath.Dir(parentDir)
				dbPath = filepath.Join(baseDir, "vector_db")
			} else {
				// 否则在同级创建
				// /Users/.../res -> /Users/.../res/vector_db
				dbPath = filepath.Join(parentDir, "vector_db")
			}
			log.Infow("使用基于 resource.image_path 计算的向量数据库路径", "image_path", imagePath, "vector_db_path", dbPath)
		} else {
			// 如果都没配置，使用默认路径
			dbPath = "./data/vector_db"
			log.Infow("使用默认向量数据库路径", "path", dbPath)
		}
	} else {
		log.Infow("使用配置的向量数据库路径", "path", dbPath)
	}

	// 创建 ConfigReader，用于从 Redis → MySQL → Viper 读取配置
	configBiz := b.Configs()
	configReader := config.NewConfigReader(configBiz)

	ragService, err := ragbiz.NewRagService(b.Ali(), configReader, dbPath)
	if err != nil {
		// RAG服务初始化失败不影响系统启动，只记录错误
		// 后续调用时会检查 ragService 是否为 nil
		// log.Errorw("初始化RAG服务失败", "error", err)
	} else {
		b.ragService = ragService
	}

	// 初始化SOP服务
	sopExecutor := sopbiz.NewSopExecutor(b.ds)
	b.sopService = sopbiz.NewSopBiz(b.ds, sopExecutor)

	// 初始化销售 RAG 服务
	// 使用 DashVector 向量库 (Migrated from VikingDB)
	dashEndpoint := viper.GetString("ali.dashvector.endpoint")
	dashApiKey := viper.GetString("ali.dashvector.api_key")
	if dashApiKey == "" {
		dashApiKey = viper.GetString("ali.api_key") // Fallback to common key
	}
	dashCollection := viper.GetString("ali.dashvector.collection")
	if dashCollection == "" {
		dashCollection = "sales_rag"
	}

	var vStore port.VectorStore

	// Define Embedder using Qwen
	embedder := func(ctx context.Context, text string) ([]float32, error) {
		return b.Ali().QianwenEmbedding(text)
	}

	if dashEndpoint != "" {
		vStore = adapter.NewDashVectorStore(dashEndpoint, dashApiKey, dashCollection, embedder)
		log.Infow("Initialized DashVector store", "endpoint", dashEndpoint, "collection", dashCollection)
	} else {
		log.Warnw("DashVector endpoint not configured, falling back to ChromemStore for local dev")
		// Fallback to local store
		salesDBPath := filepath.Join(filepath.Dir(filepath.Dir(viper.GetString("resource.image_path"))), "sales_vector_db")
		vStore, _ = adapter.NewChromemStore(salesDBPath, "sales_knowledge", embedder)
	}

	// 初始化 LLM 意图路由器（V2: 使用 DMXAPI qwen-turbo-latest）
	llmRouter := adapter.NewLLMRouter()

	// Initialize Pipeline Components
	parser := adapter.NewEnhancedParser()
	splitter := salesragservice.NewMarkdownSplitter(salesragservice.SplitterConfig{
		MaxChunkSize: 1000,
		MinChunkSize: 100,
	})
	tagger := salesragservice.NewContentTagger()

	// Initialize Ingestion Pipeline (托管模式下不需要传 embedder)
	pipeline := salesragservice.NewIngestionPipeline(parser, splitter, tagger, b.ds.KnowledgeDocuments(), vStore, b.ds.KnowledgeChunks())

	// 业务逻辑实现（使用 LLMRouter）
	salesRAGSvc := salesragservice.NewSalesRAGService(vStore, llmRouter)

	// 创建 SalesSessionStore
	salesSessionStore := store.NewSalesSessionStore(b.ds.DB())

	b.salesRAGService = salesrag.NewSalesRAGBiz(b.ds, pipeline, salesRAGSvc, b.Volc(), salesSessionStore, parser)

	return b
}

// Users 返回一个实现了 UserBiz 接口的实例.
func (b *biz) Users() user.UserBiz {
	return user.New(b.ds)
}

func (b *biz) Images() image.ImageBiz {
	return image.New(b.ds)
}

func (b *biz) Cards() card.CardBiz {
	return card.New(b.ds)
}

func (b *biz) Books() book.BookBiz {
	return book.New(b.ds)
}

func (b *biz) Categories() category.CategoryBiz {
	return category.New(b.ds)
}

func (b *biz) Templates() template.TemplateBiz {
	return template.New(b.ds)
}

func (b *biz) Feedbacks() feedback.FeedbackBiz {
	return feedback.New(b.ds)
}

func (b *biz) Baidu() baidu.BaiduBiz {
	return baidu.New(
		viper.GetString("baidu.api_key"),
		viper.GetString("baidu.secret_key"),
	)
}

func (b *biz) Wechat() wechat.WechatBiz {
	return wechat.New(b.ds)
}

func (b *biz) Ali() ali.AliBiz {
	return ali.NewAliBiz(b.ds)
}

func (b *biz) Volc() volc.VolcBiz {
	return volc.NewVolcBiz(b.ds)
}

func (b *biz) Order() order.OrderBiz {
	return order.NewOrderBiz(b.ds, b.Users(), b.AccountRecords())
}

func (b *biz) AccountRecords() accountrecordbiz.AccountRecordBiz {
	return accountrecordbiz.NewAccountRecordBiz(b.ds)
}

func (b *biz) Pagination() pagination.PaginationBiz {
	return pagination.NewPaginationBiz()
}

func (b *biz) Chats() chat.ChatBiz {
	return chat.New(b.ds, b.Users(), b.Ali(), b.Volc(), b.Rag())
}

func (b *biz) Article() article.IArticleBiz {
	return article.NewArticleBiz(b.ds.Article())
}

func (b *biz) Admin() admin.IAdminBiz {
	return admin.NewAdminBiz(b.ds.Admin())
}

func (b *biz) AdminAccounts() adminaccountbiz.AdminAccountBiz {
	return adminaccountbiz.NewAdminAccountBiz(b.ds)
}

func (b *biz) Configs() config.ConfigBiz {
	return config.New(b.ds)
}

func (b *biz) Payments() payment.PaymentBiz {
	return payment.NewPaymentBiz(b.ds)
}

func (b *biz) Rag() *ragbiz.RagService {
	return b.ragService
}

func (b *biz) Sop() sopbiz.ISopBiz {
	return b.sopService
}

func (b *biz) Customers() customerbiz.ICustomerBiz {
	return customerbiz.New(b.ds)
}

func (b *biz) SalesRAG() salesrag.SalesRAGBiz {
	return b.salesRAGService
}
