package store

//go:generate mockgen -destination mock_store.go -package store github.com/marmotedu/miniblog/internal/miniblog/store IStore,UserStore,PostStore

import (
	"sync"

	"gorm.io/gorm"
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
