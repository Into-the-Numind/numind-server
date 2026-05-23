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
	SopVisibilityGrant() ISopVisibilityGrantStore
	ChatbotVisibilityGrant() IChatbotVisibilityGrantStore
	SalesAgentOwners() ISalesAgentOwnerStore
	AgentRuns() IAgentRunStore
	ToolDefinitions() IToolDefinitionStore
	ToolFactoryRegistries() IToolFactoryRegistryStore
	AgentSandboxSessions() IAgentSandboxSessionStore
	AgentDefinitions() IAgentDefinitionStore
	SkillTemplates() ISkillTemplateStore
	AgentPermissions() IAgentPermissionStore
	AgentSessionMemories() IAgentSessionMemoryStore // #7 memory-system L1 短期记忆
	UserGlobalMemories() IUserGlobalMemoryStore     // #7 memory-system L2 长期记忆 Notepad
	Compliance() IComplianceStore                   // #13 agent-mode-compliance-3layer
	// V1.5 板块 2 task 2.1（context-management V2）— 平行重做，V1 IAgentRunStore 不动
	CompactV2() IAgentCompactV2Store
	ToolArtifact() IAgentToolArtifactStore
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

// SopVisibilityGrant 返回一个实现了 ISopVisibilityGrantStore 接口的实例.
func (ds *datastore) SopVisibilityGrant() ISopVisibilityGrantStore {
	return NewSopVisibilityGrantStore(ds.db)
}

// ChatbotVisibilityGrant 返回一个实现了 IChatbotVisibilityGrantStore 接口的实例.
func (ds *datastore) ChatbotVisibilityGrant() IChatbotVisibilityGrantStore {
	return NewChatbotVisibilityGrantStore(ds.db)
}

// SalesAgentOwners 返回一个实现了 ISalesAgentOwnerStore 接口的实例
func (ds *datastore) SalesAgentOwners() ISalesAgentOwnerStore {
	return NewSalesAgentOwnerStore(ds.db)
}

// AgentRuns 返回一个实现了 IAgentRunStore 接口的实例.
func (ds *datastore) AgentRuns() IAgentRunStore {
	return newAgentRunStore(ds.db)
}

// ToolDefinitions 返回一个实现了 IToolDefinitionStore 接口的实例.
func (ds *datastore) ToolDefinitions() IToolDefinitionStore {
	return newToolDefinitionStore(ds.db)
}

// ToolFactoryRegistries 返回一个实现了 IToolFactoryRegistryStore 接口的实例.
func (ds *datastore) ToolFactoryRegistries() IToolFactoryRegistryStore {
	return newToolFactoryRegistryStore(ds.db)
}

// AgentSandboxSessions 返回一个实现了 IAgentSandboxSessionStore 接口的实例（#4 sandbox-integration）。
func (ds *datastore) AgentSandboxSessions() IAgentSandboxSessionStore {
	return newAgentSandboxSessionStore(ds.db)
}

// AgentDefinitions 返回一个实现了 IAgentDefinitionStore 接口的实例。
func (ds *datastore) AgentDefinitions() IAgentDefinitionStore {
	return newAgentDefinitionStore(ds.db)
}

// SkillTemplates 返回一个实现了 ISkillTemplateStore 接口的实例。
func (ds *datastore) SkillTemplates() ISkillTemplateStore {
	return newSkillTemplateStore(ds.db)
}

// AgentPermissions 返回一个实现了 IAgentPermissionStore 接口的实例（#6 permission-pipeline）。
func (ds *datastore) AgentPermissions() IAgentPermissionStore {
	return newAgentPermissionStore(ds.db)
}

// AgentSessionMemories 返回一个实现了 IAgentSessionMemoryStore 接口的实例（#7 memory-system）。
func (ds *datastore) AgentSessionMemories() IAgentSessionMemoryStore {
	return NewAgentSessionMemoryStore(ds.db)
}

// UserGlobalMemories 返回一个实现了 IUserGlobalMemoryStore 接口的实例（#7 memory-system）。
func (ds *datastore) UserGlobalMemories() IUserGlobalMemoryStore {
	return NewUserGlobalMemoryStore(ds.db)
}

// Compliance 返回一个实现了 IComplianceStore 接口的实例（#13 agent-mode-compliance-3layer）。
func (ds *datastore) Compliance() IComplianceStore {
	return newCompliance(ds.db)
}

// CompactV2 返回 IAgentCompactV2Store 实现（V1.5 板块 2 task 2.1 — context-management V2）。
// 平行重做：V1 `AgentRuns()` 完全保留不动，本方法只读写 *_v2 列。
func (ds *datastore) CompactV2() IAgentCompactV2Store {
	return newAgentCompactV2Store(ds.db)
}

// ToolArtifact 返回 IAgentToolArtifactStore 实现（V1.5 板块 2 task 2.1）。
// task 2.2 起会被 L0 tool result 写盘代码使用。
func (ds *datastore) ToolArtifact() IAgentToolArtifactStore {
	return newAgentToolArtifactStore(ds.db)
}
