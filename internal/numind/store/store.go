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
	UserMemoryProfiles() IUserMemoryProfileStore    // agent-mode-v15-memory-layer-a (Task 3.2) per-user 画像 + dialectic cache
	UserMemoryFacts() IUserMemoryFactStore          // agent-mode-v15-memory-layer-a (Task 3.2) per-user fact 列表
	UserMemoryDigests() IMemoryDigestStore          // agent-mode-v15-memory-layer-a (Task 3.8) 4 张 daily/weekly/monthly/quarterly digest
	AgentMessageSearches() IAgentMessageSearchStore // agent-mode-v15-memory-layer-a (Task 3.5) FULLTEXT search 索引
	Compliance() IComplianceStore                   // #13 agent-mode-compliance-3layer
	// V1.5 v2-compact-adapter-integration — V2 L0 工具写盘表（compact_dead_schema_cleanup
	// 删了 IAgentCompactV2Store，V2 prevention 状态在 adapter compactor 内存里自管）。
	ToolArtifact() IAgentToolArtifactStore
	AgentAttachments() IAgentAttachmentStore // V1.5 multimodal fallback task 1.2
	Marketplaces() IMarketplaceStore         // agent-mode-v2-skill-marketplace — skill_marketplace + skill_subscription
	Announcements() IAnnouncementStore       // notification-center — 公告/问卷通知中心
	Documents() IDocumentStore               // document-system — AI 生成产物的可编辑文档
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

// UserMemoryProfiles 返回一个实现了 IUserMemoryProfileStore 接口的实例
// （agent-mode-v15-memory-layer-a Task 3.2）。
func (ds *datastore) UserMemoryProfiles() IUserMemoryProfileStore {
	return NewUserMemoryProfileStore(ds.db)
}

// UserMemoryFacts 返回一个实现了 IUserMemoryFactStore 接口的实例
// （agent-mode-v15-memory-layer-a Task 3.2）。
func (ds *datastore) UserMemoryFacts() IUserMemoryFactStore {
	return NewUserMemoryFactStore(ds.db)
}

// UserMemoryDigests 返回一个实现了 IMemoryDigestStore 接口的实例
// （agent-mode-v15-memory-layer-a Task 3.8 分层时间感知 daily/weekly/monthly/quarterly 4 张表）。
func (ds *datastore) UserMemoryDigests() IMemoryDigestStore {
	return NewMemoryDigestStore(ds.db)
}

// AgentMessageSearches 返回一个实现了 IAgentMessageSearchStore 接口的实例
// （agent-mode-v15-memory-layer-a Task 3.5 FULLTEXT 中文搜索）。
func (ds *datastore) AgentMessageSearches() IAgentMessageSearchStore {
	return NewAgentMessageSearchStore(ds.db)
}

// AgentAttachments 返回一个实现了 IAgentAttachmentStore 接口的实例（V1.5 task 1.2）。
func (ds *datastore) AgentAttachments() IAgentAttachmentStore {
	return newAgentAttachmentStore(ds.db)
}

// Marketplaces 返回一个实现了 IMarketplaceStore 接口的实例
// （agent-mode-v2-skill-marketplace — skill_marketplace + skill_subscription）。
func (ds *datastore) Marketplaces() IMarketplaceStore {
	return NewMarketplaceStore(ds.db)
}

// Compliance 返回一个实现了 IComplianceStore 接口的实例（#13 agent-mode-compliance-3layer）。
func (ds *datastore) Compliance() IComplianceStore {
	return newCompliance(ds.db)
}

// ToolArtifact 返回 IAgentToolArtifactStore 实现（V1.5 板块 2 task 2.1）。
// task 2.2 起会被 L0 tool result 写盘代码使用。
func (ds *datastore) ToolArtifact() IAgentToolArtifactStore {
	return newAgentToolArtifactStore(ds.db)
}

// Announcements 返回一个实现了 IAnnouncementStore 接口的实例（notification-center）。
func (ds *datastore) Announcements() IAnnouncementStore {
	return NewAnnouncementStore(ds.db)
}

// Documents 返回一个实现了 IDocumentStore 接口的实例（document-system）。
func (ds *datastore) Documents() IDocumentStore {
	return newDocumentStore(ds.db)
}
