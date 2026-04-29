package store

//go:generate mockgen -destination mock_store.go -package store github.com/marmotedu/miniblog/internal/miniblog/store IStore,UserStore,PostStore

import (
	"sync"

	"gorm.io/gorm"

	membstore "numind-server/internal/numind/store/membership"
)

var (
	once sync.Once
	// 全局变量，方便其它包直接调用已初始化好的 S 实例.
	S *datastore
)

// IStore 定义了 Store 层需要实现的方法.
type IStore interface {
	DB() *gorm.DB
	Users() UserStore
	Configs() ConfigStore
	Sop() ISopStore
	Customers() ICustomerStore
	KnowledgeDocuments() KnowledgeDocumentStore
	KnowledgeChunks() KnowledgeChunkStore
	LanguageStyles() LanguageStyleStore
	Billing() BillingStore
	Credits() CreditStore
	Orders() OrderStore
	Monitor() IMonitorStore
	KnowledgeBase() IKnowledgeBaseStore
	ChatbotConfig() IChatbotConfigStore
	ChatbotSession() IChatbotSessionStore
	LLMProvider() ILLMProviderStore
	UserModelPreference() IUserModelPreferenceStore
	Membership() membstore.IMembershipStore
}

// datastore 是 IStore 的一个具体实现.
type datastore struct {
	db *gorm.DB
}

// 确保 datastore 实现了 IStore 接口.
var _ IStore = (*datastore)(nil)

// NewStore 创建一个 IStore 类型的实例.
func NewStore(db *gorm.DB) *datastore {
	// 确保 S 只被初始化一次
	once.Do(func() {
		S = &datastore{db}
	})

	return S
}

// NewTestStore creates a fresh IStore instance backed by the given DB without
// the singleton constraint. Use only in tests.
func NewTestStore(db *gorm.DB) IStore {
	return &datastore{db: db}
}

// NewTestDataStore is identical to NewTestStore but returns the concrete
// *datastore pointer so tests can assign it to the package-level `S` variable
// (which is typed `*datastore`, not `IStore`). Use only in tests that need to
// exercise code paths reading `store.S` directly (e.g. middleware.FeaturePermission).
func NewTestDataStore(db *gorm.DB) *datastore {
	return &datastore{db: db}
}

// DB 返回存储在 datastore 中的 *gorm.DB.
func (ds *datastore) DB() *gorm.DB {
	return ds.db
}

// Users 返回一个实现了 UserStore 接口的实例.
func (ds *datastore) Users() UserStore {
	return newUsers(ds.db)
}

// Configs 返回一个实现了 ConfigStore 接口的实例.
func (ds *datastore) Configs() ConfigStore {
	return NewConfigStore(ds.db)
}

// Sop 返回一个实现了 ISopStore 接口的实例.
func (ds *datastore) Sop() ISopStore {
	return NewSopStore(ds.db)
}

// Customers 返回一个实现了 ICustomerStore 接口的实例.
func (ds *datastore) Customers() ICustomerStore {
	return NewCustomerStore(ds.db)
}

// KnowledgeDocuments 返回一个实现了 KnowledgeDocumentStore 接口的实例.
func (ds *datastore) KnowledgeDocuments() KnowledgeDocumentStore {
	return newKnowledgeDocuments(ds.db)
}

// KnowledgeChunks 返回一个实现了 KnowledgeChunkStore 接口的实例.
func (ds *datastore) KnowledgeChunks() KnowledgeChunkStore {
	return newKnowledgeChunks(ds.db)
}

// LanguageStyles 返回一个实现了 LanguageStyleStore 接口的实例.
func (ds *datastore) LanguageStyles() LanguageStyleStore {
	return NewLanguageStyleStore(ds.db)
}

// Billing 返回一个实现了 BillingStore 接口的实例.
func (ds *datastore) Billing() BillingStore {
	return newBillingStore(ds.db)
}

// Credits 返回一个实现了 CreditStore 接口的实例.
func (ds *datastore) Credits() CreditStore {
	return newCreditStore(ds.db)
}

// Orders 返回一个实现了 OrderStore 接口的实例.
func (ds *datastore) Orders() OrderStore {
	return newOrderStore(ds.db)
}

// Monitor 返回一个实现了 IMonitorStore 接口的实例.
func (ds *datastore) Monitor() IMonitorStore {
	return NewMonitorStore(ds.db)
}

// KnowledgeBase 返回一个实现了 IKnowledgeBaseStore 接口的实例.
func (ds *datastore) KnowledgeBase() IKnowledgeBaseStore {
	return NewKnowledgeBaseStore(ds.db)
}

// ChatbotConfig 返回一个实现了 IChatbotConfigStore 接口的实例.
func (ds *datastore) ChatbotConfig() IChatbotConfigStore {
	return NewChatbotConfigStore(ds.db)
}

// ChatbotSession 返回一个实现了 IChatbotSessionStore 接口的实例.
func (ds *datastore) ChatbotSession() IChatbotSessionStore {
	return NewChatbotSessionStore(ds.db)
}

// LLMProvider 返回一个实现了 ILLMProviderStore 接口的实例.
func (ds *datastore) LLMProvider() ILLMProviderStore {
	return NewLLMProviderStore(ds.db)
}

// UserModelPreference 返回一个实现了 IUserModelPreferenceStore 接口的实例.
func (ds *datastore) UserModelPreference() IUserModelPreferenceStore {
	return NewUserModelPreferenceStore(ds.db)
}

// Membership 返回一个实现了 IMembershipStore 接口的实例.
func (ds *datastore) Membership() membstore.IMembershipStore {
	return membstore.NewMembershipStore(ds.db)
}
