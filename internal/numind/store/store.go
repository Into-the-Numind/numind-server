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
	Images() ImageStore
	Cards() CardStore
	Books() BookStore
	Orders() OrderStore
	Categories() CategoryStore
	Templates() TemplateStore
	Feedbacks() FeedbackStore
	Chats() ChatStore
	AccountRecords() AccountRecordStore
	Article() IArticleStore
	Admin() IAdminStore
	Configs() ConfigStore
	Payments() PaymentStore
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

func (ds *datastore) Images() ImageStore {
	return NewImageStore(ds.db)
}

func (ds *datastore) Cards() CardStore {
	return NewCardStore(ds.db)
}

func (ds *datastore) Books() BookStore {
	return NewBookStore(ds.db)
}

func (ds *datastore) Orders() OrderStore {
	return NewOrderStore(ds.db)
}

func (ds *datastore) Categories() CategoryStore {
	return NewCategoryStore(ds.db)
}

func (ds *datastore) Templates() TemplateStore {
	return NewTemplateStore(ds.db)
}

func (ds *datastore) Feedbacks() FeedbackStore {
	return newFeedbacks(ds.db)
}

func (ds *datastore) Chats() ChatStore {
	return NewChatStore(ds.db)
}

func (ds *datastore) AccountRecords() AccountRecordStore {
	return NewAccountRecordStore(ds.db)
}

func (ds *datastore) Article() IArticleStore {
	return NewArticleStore(ds.db)
}

func (ds *datastore) Admin() IAdminStore {
	return NewAdminStore(ds.db)
}

func (ds *datastore) Configs() ConfigStore {
	return NewConfigStore(ds.db)
}

func (ds *datastore) Payments() PaymentStore {
	return NewPaymentStore(ds.db)
}
