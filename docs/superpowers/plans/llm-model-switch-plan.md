# LLM 模型切换与多供应商智能路由 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users select LLM models (Claude/Gemini/DeepSeek/GPT) for SOP execution and chatbot conversations, with multi-provider intelligent routing and admin management.

**Architecture:** Three new DB tables (llm_provider, llm_model, llm_model_provider) + user preference table. LLMRouter biz layer resolves logical model → provider route with fallback. DMXAPIClient generalized to accept dynamic baseURL/apiKey. Frontend ModelSelector component shared by chatbot and SOP.

**Tech Stack:** Go/Gin/GORM (backend), Vue 3/TypeScript/Pinia (frontend), MySQL 8.0

**Repos:** numind-server (Tasks 1-9), numind-web-v3 (Tasks 10-12), numind-admin-web (Tasks 13-14)

**Spec:** `numind-server/docs/superpowers/specs/2026-04-10-llm-model-switch-design.md`

---

### Task 1: Database Migration + GORM Models

**Files:**
- Create: `numind-server/migrations/20260410_000001_add_llm_routing_tables.sql`
- Create: `numind-server/internal/pkg/model/llm.go`

- [ ] **Step 1: Create migration SQL**

Create `numind-server/migrations/20260410_000001_add_llm_routing_tables.sql`:

```sql
-- LLM 模型切换与多供应商路由表
-- 参考 spec: docs/superpowers/specs/2026-04-10-llm-model-switch-design.md §1

-- 1. LLM 供应商表
CREATE TABLE IF NOT EXISTS llm_provider (
    id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    name         VARCHAR(50) NOT NULL UNIQUE,
    display_name VARCHAR(100) NOT NULL,
    base_url     VARCHAR(255) NOT NULL,
    api_key      VARCHAR(255) NOT NULL,
    is_active    TINYINT(1) DEFAULT 1,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 2. LLM 逻辑模型表
CREATE TABLE IF NOT EXISTS llm_model (
    id                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    model_key         VARCHAR(100) NOT NULL UNIQUE,
    display_name      VARCHAR(100) NOT NULL,
    is_thinking       TINYINT(1) DEFAULT 0,
    base_model_id     BIGINT UNSIGNED,
    supports_thinking TINYINT(1) DEFAULT 0,
    icon              VARCHAR(50),
    sort_order        INT DEFAULT 0,
    is_active         TINYINT(1) DEFAULT 1,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_base_model (base_model_id),
    CONSTRAINT fk_base_model FOREIGN KEY (base_model_id) REFERENCES llm_model(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 3. 模型×供应商路由映射
CREATE TABLE IF NOT EXISTS llm_model_provider (
    id                    BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    model_id              BIGINT UNSIGNED NOT NULL,
    provider_id           BIGINT UNSIGNED NOT NULL,
    provider_model_id     VARCHAR(100) NOT NULL,
    priority              INT DEFAULT 0,
    input_price_per_mtok  DECIMAL(10,4) DEFAULT 0,
    output_price_per_mtok DECIMAL(10,4) DEFAULT 0,
    is_active             TINYINT(1) DEFAULT 1,
    created_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_model_provider (model_id, provider_id),
    INDEX idx_mp_model_active (model_id, is_active, priority),
    CONSTRAINT fk_mp_model FOREIGN KEY (model_id) REFERENCES llm_model(id),
    CONSTRAINT fk_mp_provider FOREIGN KEY (provider_id) REFERENCES llm_provider(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 4. 用户模型偏好表
CREATE TABLE IF NOT EXISTS user_model_preference (
    id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id    BIGINT UNSIGNED NOT NULL,
    feature    VARCHAR(20) NOT NULL,
    model_key  VARCHAR(100) NOT NULL,
    thinking   TINYINT(1) DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user_feature (user_id, feature)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 5. Seed: 初始模型数据（4 基础 + 4 thinking）
INSERT INTO llm_model (model_key, display_name, is_thinking, base_model_id, supports_thinking, icon, sort_order) VALUES
('claude-sonnet-4-6', 'Claude Sonnet 4.6', 0, NULL, 1, 'claude', 1),
('gemini-3.1-pro-preview', 'Gemini 3.1 Pro', 0, NULL, 1, 'gemini', 2),
('deepseek-v3.2', 'DeepSeek V3.2', 0, NULL, 1, 'deepseek', 3),
('gpt-5.4', 'GPT 5.4', 0, NULL, 1, 'openai', 4);

-- Thinking 变体（base_model_id 通过子查询填充）
INSERT INTO llm_model (model_key, display_name, is_thinking, base_model_id, supports_thinking, icon, sort_order) VALUES
('claude-sonnet-4-6-thinking', 'Claude Sonnet 4.6 Thinking', 1, (SELECT id FROM llm_model WHERE model_key='claude-sonnet-4-6'), 0, 'claude', 11),
('gemini-3.1-pro-preview-thinking', 'Gemini 3.1 Pro Thinking', 1, (SELECT id FROM llm_model WHERE model_key='gemini-3.1-pro-preview'), 0, 'gemini', 12),
('deepseek-v3.2-thinking', 'DeepSeek V3.2 Thinking', 1, (SELECT id FROM llm_model WHERE model_key='deepseek-v3.2'), 0, 'deepseek', 13),
('gpt-5.4-thinking', 'GPT 5.4 Thinking', 1, (SELECT id FROM llm_model WHERE model_key='gpt-5.4'), 0, 'openai', 14);
```

- [ ] **Step 2: Create GORM models**

Create `numind-server/internal/pkg/model/llm.go`:

```go
package model

import "time"

// LLMProvider LLM 供应商
type LLMProvider struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string    `gorm:"size:50;not null;uniqueIndex" json:"name"`
	DisplayName string    `gorm:"size:100;not null" json:"display_name"`
	BaseURL     string    `gorm:"size:255;not null" json:"base_url"`
	APIKey      string    `gorm:"size:255;not null" json:"-"`
	IsActive    bool      `gorm:"default:true" json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (LLMProvider) TableName() string { return "llm_provider" }

// MaskedAPIKey 返回脱敏的 API key（仅显示后 4 位）
func (p *LLMProvider) MaskedAPIKey() string {
	if len(p.APIKey) <= 4 {
		return "****"
	}
	return "****" + p.APIKey[len(p.APIKey)-4:]
}

// LLMModel LLM 逻辑模型
type LLMModel struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelKey          string    `gorm:"size:100;not null;uniqueIndex" json:"model_key"`
	DisplayName      string    `gorm:"size:100;not null" json:"display_name"`
	IsThinking       bool      `gorm:"default:false" json:"is_thinking"`
	BaseModelID      *uint64   `gorm:"index:idx_base_model" json:"base_model_id"`
	SupportsThinking bool      `gorm:"default:false" json:"supports_thinking"`
	Icon             string    `gorm:"size:50" json:"icon"`
	SortOrder        int       `gorm:"default:0" json:"sort_order"`
	IsActive         bool      `gorm:"default:true" json:"is_active"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (LLMModel) TableName() string { return "llm_model" }

// LLMModelProvider 模型×供应商路由映射
type LLMModelProvider struct {
	ID                uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ModelID           uint64    `gorm:"not null;uniqueIndex:uk_model_provider" json:"model_id"`
	ProviderID        uint64    `gorm:"not null;uniqueIndex:uk_model_provider" json:"provider_id"`
	ProviderModelID   string    `gorm:"size:100;not null" json:"provider_model_id"`
	Priority          int       `gorm:"default:0" json:"priority"`
	InputPricePerMTok float64   `gorm:"type:decimal(10,4);default:0" json:"input_price_per_mtok"`
	OutputPricePerMTok float64  `gorm:"type:decimal(10,4);default:0" json:"output_price_per_mtok"`
	IsActive          bool      `gorm:"default:true" json:"is_active"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`

	// Preload 关系
	Provider *LLMProvider `gorm:"foreignKey:ProviderID" json:"provider,omitempty"`
	Model    *LLMModel    `gorm:"foreignKey:ModelID" json:"model,omitempty"`
}

func (LLMModelProvider) TableName() string { return "llm_model_provider" }

// UserModelPreference 用户模型偏好
type UserModelPreference struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID    uint64    `gorm:"not null;uniqueIndex:uk_user_feature" json:"user_id"`
	Feature   string    `gorm:"size:20;not null;uniqueIndex:uk_user_feature" json:"feature"`
	ModelKey  string    `gorm:"size:100;not null" json:"model_key"`
	Thinking  bool      `gorm:"default:false" json:"thinking"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (UserModelPreference) TableName() string { return "user_model_preference" }
```

- [ ] **Step 3: Run migration on local DB**

```bash
cd numind-server
# 连接本地 MySQL 执行 migration
mysql -u root -p numind < migrations/20260410_000001_add_llm_routing_tables.sql
```

- [ ] **Step 4: Verify compilation**

```bash
cd numind-server && go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add migrations/20260410_000001_add_llm_routing_tables.sql internal/pkg/model/llm.go
git commit -m "feat: add LLM routing tables and GORM models"
```

---

### Task 2: Store Layer — LLM Provider, Model, ModelProvider, Preference

**Files:**
- Create: `numind-server/internal/numind/store/llm_provider.go`
- Create: `numind-server/internal/numind/store/llm_model.go`
- Create: `numind-server/internal/numind/store/llm_model_provider.go`
- Create: `numind-server/internal/numind/store/llm_preference.go`
- Modify: `numind-server/internal/numind/store/store.go` — register new sub-stores in IStore

- [ ] **Step 1: Create ILLMProviderStore interface and implementation**

Create `numind-server/internal/numind/store/llm_provider.go`:

```go
package store

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ILLMProviderStore LLM 供应商数据存储接口
type ILLMProviderStore interface {
	List(ctx context.Context, offset, limit int) ([]model.LLMProvider, int64, error)
	Get(ctx context.Context, id uint64) (*model.LLMProvider, error)
	Create(ctx context.Context, p *model.LLMProvider) error
	Update(ctx context.Context, p *model.LLMProvider) error
	Delete(ctx context.Context, id uint64) error
	ListActive(ctx context.Context) ([]model.LLMProvider, error)
}

type llmProviderStore struct {
	db *gorm.DB
}

var _ ILLMProviderStore = (*llmProviderStore)(nil)

// NewLLMProviderStore 创建供应商存储实例
func NewLLMProviderStore(db *gorm.DB) ILLMProviderStore {
	return &llmProviderStore{db: db}
}

func (s *llmProviderStore) List(ctx context.Context, offset, limit int) ([]model.LLMProvider, int64, error) {
	var total int64
	var items []model.LLMProvider
	d := s.db.WithContext(ctx).Model(&model.LLMProvider{})
	if err := d.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := d.Offset(offset).Limit(limit).Order("id ASC").Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *llmProviderStore) Get(ctx context.Context, id uint64) (*model.LLMProvider, error) {
	var p model.LLMProvider
	if err := s.db.WithContext(ctx).First(&p, id).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *llmProviderStore) Create(ctx context.Context, p *model.LLMProvider) error {
	return s.db.WithContext(ctx).Create(p).Error
}

func (s *llmProviderStore) Update(ctx context.Context, p *model.LLMProvider) error {
	return s.db.WithContext(ctx).Save(p).Error
}

func (s *llmProviderStore) Delete(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Delete(&model.LLMProvider{}, id).Error
}

func (s *llmProviderStore) ListActive(ctx context.Context) ([]model.LLMProvider, error) {
	var items []model.LLMProvider
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}
```

- [ ] **Step 2: Create ILLMModelStore interface and implementation**

Create `numind-server/internal/numind/store/llm_model.go`:

```go
package store

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ILLMModelStore LLM 模型数据存储接口
type ILLMModelStore interface {
	List(ctx context.Context, offset, limit int) ([]model.LLMModel, int64, error)
	Get(ctx context.Context, id uint64) (*model.LLMModel, error)
	GetByKey(ctx context.Context, key string) (*model.LLMModel, error)
	Create(ctx context.Context, m *model.LLMModel) error
	Update(ctx context.Context, m *model.LLMModel) error
	Delete(ctx context.Context, id uint64) error
	ListActiveBase(ctx context.Context) ([]model.LLMModel, error)
	GetThinkingVariant(ctx context.Context, baseModelID uint64) (*model.LLMModel, error)
	GetDefaultModel(ctx context.Context) (*model.LLMModel, error)
}

type llmModelStore struct {
	db *gorm.DB
}

var _ ILLMModelStore = (*llmModelStore)(nil)

// NewLLMModelStore 创建模型存储实例
func NewLLMModelStore(db *gorm.DB) ILLMModelStore {
	return &llmModelStore{db: db}
}

func (s *llmModelStore) List(ctx context.Context, offset, limit int) ([]model.LLMModel, int64, error) {
	var total int64
	var items []model.LLMModel
	d := s.db.WithContext(ctx).Model(&model.LLMModel{})
	if err := d.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := d.Offset(offset).Limit(limit).Order("sort_order ASC, id ASC").Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *llmModelStore) Get(ctx context.Context, id uint64) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := s.db.WithContext(ctx).First(&m, id).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *llmModelStore) GetByKey(ctx context.Context, key string) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := s.db.WithContext(ctx).Where("model_key = ?", key).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *llmModelStore) Create(ctx context.Context, m *model.LLMModel) error {
	return s.db.WithContext(ctx).Create(m).Error
}

func (s *llmModelStore) Update(ctx context.Context, m *model.LLMModel) error {
	return s.db.WithContext(ctx).Save(m).Error
}

func (s *llmModelStore) Delete(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Delete(&model.LLMModel{}, id).Error
}

// ListActiveBase 返回可用的基础模型（is_thinking=0, is_active=1），按 sort_order 排序
func (s *llmModelStore) ListActiveBase(ctx context.Context) ([]model.LLMModel, error) {
	var items []model.LLMModel
	if err := s.db.WithContext(ctx).
		Where("is_active = ? AND is_thinking = ?", true, false).
		Order("sort_order ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// GetThinkingVariant 根据基础模型 ID 查找 thinking 变体
func (s *llmModelStore) GetThinkingVariant(ctx context.Context, baseModelID uint64) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := s.db.WithContext(ctx).
		Where("base_model_id = ? AND is_thinking = ? AND is_active = ?", baseModelID, true, true).
		First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// GetDefaultModel 返回系统默认模型（sort_order 最小的活跃基础模型）
func (s *llmModelStore) GetDefaultModel(ctx context.Context) (*model.LLMModel, error) {
	var m model.LLMModel
	if err := s.db.WithContext(ctx).
		Where("is_active = ? AND is_thinking = ?", true, false).
		Order("sort_order ASC").
		First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}
```

- [ ] **Step 3: Create ILLMModelProviderStore interface and implementation**

Create `numind-server/internal/numind/store/llm_model_provider.go`:

```go
package store

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// ILLMModelProviderStore 模型供应商路由映射数据存储接口
type ILLMModelProviderStore interface {
	ListByModel(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error)
	ListActiveByModel(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error)
	Get(ctx context.Context, id uint64) (*model.LLMModelProvider, error)
	Create(ctx context.Context, mp *model.LLMModelProvider) error
	Update(ctx context.Context, mp *model.LLMModelProvider) error
	Delete(ctx context.Context, id uint64) error
}

type llmModelProviderStore struct {
	db *gorm.DB
}

var _ ILLMModelProviderStore = (*llmModelProviderStore)(nil)

// NewLLMModelProviderStore 创建路由映射存储实例
func NewLLMModelProviderStore(db *gorm.DB) ILLMModelProviderStore {
	return &llmModelProviderStore{db: db}
}

func (s *llmModelProviderStore) ListByModel(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error) {
	var items []model.LLMModelProvider
	if err := s.db.WithContext(ctx).
		Preload("Provider").
		Where("model_id = ?", modelID).
		Order("priority ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListActiveByModel 返回该模型的可用路由（按 priority 排序），Preload Provider
func (s *llmModelProviderStore) ListActiveByModel(ctx context.Context, modelID uint64) ([]model.LLMModelProvider, error) {
	var items []model.LLMModelProvider
	if err := s.db.WithContext(ctx).
		Preload("Provider", "is_active = ?", true).
		Where("model_id = ? AND is_active = ?", modelID, true).
		Order("priority ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	// 过滤掉 Provider 被禁用的路由（Preload 后 Provider 为 nil 的）
	var result []model.LLMModelProvider
	for _, item := range items {
		if item.Provider != nil {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *llmModelProviderStore) Get(ctx context.Context, id uint64) (*model.LLMModelProvider, error) {
	var mp model.LLMModelProvider
	if err := s.db.WithContext(ctx).Preload("Provider").First(&mp, id).Error; err != nil {
		return nil, err
	}
	return &mp, nil
}

func (s *llmModelProviderStore) Create(ctx context.Context, mp *model.LLMModelProvider) error {
	return s.db.WithContext(ctx).Create(mp).Error
}

func (s *llmModelProviderStore) Update(ctx context.Context, mp *model.LLMModelProvider) error {
	return s.db.WithContext(ctx).Save(mp).Error
}

func (s *llmModelProviderStore) Delete(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Delete(&model.LLMModelProvider{}, id).Error
}
```

- [ ] **Step 4: Create IUserModelPreferenceStore interface and implementation**

Create `numind-server/internal/numind/store/llm_preference.go`:

```go
package store

import (
	"context"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// IUserModelPreferenceStore 用户模型偏好数据存储接口
type IUserModelPreferenceStore interface {
	Get(ctx context.Context, userID uint64, feature string) (*model.UserModelPreference, error)
	GetAll(ctx context.Context, userID uint64) ([]model.UserModelPreference, error)
	Upsert(ctx context.Context, pref *model.UserModelPreference) error
}

type userModelPreferenceStore struct {
	db *gorm.DB
}

var _ IUserModelPreferenceStore = (*userModelPreferenceStore)(nil)

// NewUserModelPreferenceStore 创建用户偏好存储实例
func NewUserModelPreferenceStore(db *gorm.DB) IUserModelPreferenceStore {
	return &userModelPreferenceStore{db: db}
}

func (s *userModelPreferenceStore) Get(ctx context.Context, userID uint64, feature string) (*model.UserModelPreference, error) {
	var pref model.UserModelPreference
	if err := s.db.WithContext(ctx).
		Where("user_id = ? AND feature = ?", userID, feature).
		First(&pref).Error; err != nil {
		return nil, err
	}
	return &pref, nil
}

func (s *userModelPreferenceStore) GetAll(ctx context.Context, userID uint64) ([]model.UserModelPreference, error) {
	var prefs []model.UserModelPreference
	if err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Find(&prefs).Error; err != nil {
		return nil, err
	}
	return prefs, nil
}

// Upsert 创建或更新用户偏好（基于 uk_user_feature 唯一键）
func (s *userModelPreferenceStore) Upsert(ctx context.Context, pref *model.UserModelPreference) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "feature"}},
			DoUpdates: clause.AssignmentColumns([]string{"model_key", "thinking", "updated_at"}),
		}).
		Create(pref).Error
}
```

- [ ] **Step 5: Register new sub-stores in IStore**

Modify `numind-server/internal/numind/store/store.go`:

Add to `IStore` interface:
```go
LLMProvider() ILLMProviderStore
LLMModel() ILLMModelStore
LLMModelProvider() ILLMModelProviderStore
UserModelPreference() IUserModelPreferenceStore
```

Add implementations to the `datastore` struct's methods:
```go
func (ds *datastore) LLMProvider() ILLMProviderStore {
	return NewLLMProviderStore(ds.db)
}

func (ds *datastore) LLMModel() ILLMModelStore {
	return NewLLMModelStore(ds.db)
}

func (ds *datastore) LLMModelProvider() ILLMModelProviderStore {
	return NewLLMModelProviderStore(ds.db)
}

func (ds *datastore) UserModelPreference() IUserModelPreferenceStore {
	return NewUserModelPreferenceStore(ds.db)
}
```

- [ ] **Step 6: Verify compilation**

```bash
cd numind-server && go build ./...
```

- [ ] **Step 7: Commit**

```bash
git add internal/numind/store/llm_provider.go internal/numind/store/llm_model.go \
  internal/numind/store/llm_model_provider.go internal/numind/store/llm_preference.go \
  internal/numind/store/store.go
git commit -m "feat: add LLM store layer (provider, model, model_provider, preference)"
```

---

### Task 3: DMXAPIClient Generalization

**Files:**
- Modify: `numind-server/internal/pkg/llm/dmxapi_client.go`

- [ ] **Step 1: Add NewDMXAPIClientWithConfig constructor**

Add to `internal/pkg/llm/dmxapi_client.go`, right after the existing `NewDMXAPIClient()` function:

```go
// NewDMXAPIClientWithConfig 创建支持动态 baseURL 和 apiKey 的客户端（LLMRouter 使用）
func NewDMXAPIClientWithConfig(baseURL, apiKey string) *DMXAPIClient {
	return &DMXAPIClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 120 * time.Second,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}
```

- [ ] **Step 2: Add context marker to skip internal Langfuse when called via LLMRouter**

Add to `internal/pkg/llm/dmxapi_client.go`:

```go
type llmRouterCtxKey struct{}

// WithLLMRouterMark 标记此次调用来自 LLMRouter（LLMRouter 内部会自行处理 Langfuse）
func WithLLMRouterMark(ctx context.Context) context.Context {
	return context.WithValue(ctx, llmRouterCtxKey{}, true)
}

// isFromLLMRouter 检查是否由 LLMRouter 调用
func isFromLLMRouter(ctx context.Context) bool {
	v, _ := ctx.Value(llmRouterCtxKey{}).(bool)
	return v
}
```

Then wrap the existing Langfuse generation code in `ChatCompletion` and `StreamChatCompletion` with:
```go
if !isFromLLMRouter(ctx) {
    // existing Langfuse generation code
}
```

Similarly wrap the billing `RecordLLM` call:
```go
if !isFromLLMRouter(ctx) {
    if bc := billing.FromContext(ctx); bc != nil && result.Usage != nil {
        billing.RecordLLM(bc.UserID, "dmxapi", model, bc.Operation, result.Usage, bc.Meta)
    }
}
```

- [ ] **Step 3: Verify compilation and existing tests**

```bash
cd numind-server && go build ./... && go test ./internal/pkg/llm/...
```

- [ ] **Step 4: Commit**

```bash
git add internal/pkg/llm/dmxapi_client.go
git commit -m "feat: generalize DMXAPIClient with dynamic config constructor"
```

---

### Task 4: LLMRouter Service Layer

**Files:**
- Create: `numind-server/internal/numind/biz/llmrouter/types.go`
- Create: `numind-server/internal/numind/biz/llmrouter/cache.go`
- Create: `numind-server/internal/numind/biz/llmrouter/router.go`

- [ ] **Step 1: Create types.go**

Create `numind-server/internal/numind/biz/llmrouter/types.go`:

```go
package llmrouter

// ResolvedRoute 路由解析结果
type ResolvedRoute struct {
	BaseURL         string
	APIKey          string
	ProviderModelID string
	ProviderName    string
	EnableThinking  bool
}
```

- [ ] **Step 2: Create cache.go**

Create `numind-server/internal/numind/biz/llmrouter/cache.go`:

```go
package llmrouter

import (
	"sync"
	"time"

	"numind-server/internal/pkg/model"
)

const cacheTTL = 5 * time.Minute

type routerCache struct {
	mu             sync.RWMutex
	models         []model.LLMModel
	modelsExpireAt time.Time
	routes         map[uint64][]ResolvedRoute // modelID → routes
	routesExpireAt time.Time
}

func newRouterCache() *routerCache {
	return &routerCache{
		routes: make(map[uint64][]ResolvedRoute),
	}
}

func (c *routerCache) getModels() ([]model.LLMModel, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Now().Before(c.modelsExpireAt) && c.models != nil {
		return c.models, true
	}
	return nil, false
}

func (c *routerCache) setModels(models []model.LLMModel) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = models
	c.modelsExpireAt = time.Now().Add(cacheTTL)
}

func (c *routerCache) getRoutes(modelID uint64) ([]ResolvedRoute, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Now().Before(c.routesExpireAt) {
		if routes, ok := c.routes[modelID]; ok {
			return routes, true
		}
	}
	return nil, false
}

func (c *routerCache) setRoutes(modelID uint64, routes []ResolvedRoute) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routes[modelID] = routes
	c.routesExpireAt = time.Now().Add(cacheTTL)
}

// Invalidate 清除所有缓存
func (c *routerCache) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = nil
	c.modelsExpireAt = time.Time{}
	c.routes = make(map[uint64][]ResolvedRoute)
	c.routesExpireAt = time.Time{}
}
```

- [ ] **Step 3: Create router.go with Resolve and StreamChat**

Create `numind-server/internal/numind/biz/llmrouter/router.go`:

```go
package llmrouter

import (
	"context"
	"fmt"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/llm"
	"numind-server/internal/pkg/log"
)

// LLMRouter 多供应商 LLM 路由器
type LLMRouter struct {
	ds    store.IStore
	cache *routerCache
}

// NewLLMRouter 创建路由器实例
func NewLLMRouter(ds store.IStore) *LLMRouter {
	return &LLMRouter{
		ds:    ds,
		cache: newRouterCache(),
	}
}

// InvalidateCache 管理端写操作后调用，清除缓存
func (r *LLMRouter) InvalidateCache() {
	r.cache.Invalidate()
}

// Resolve 解析逻辑模型到供应商路由列表（按 priority 排序）
func (r *LLMRouter) Resolve(ctx context.Context, modelKey string, thinking bool) ([]ResolvedRoute, error) {
	// 1. 查找基础模型
	baseModel, err := r.ds.LLMModel().GetByKey(ctx, modelKey)
	if err != nil {
		return nil, fmt.Errorf("Resolve: model %q not found: %w", modelKey, err)
	}
	if !baseModel.IsActive {
		return nil, fmt.Errorf("Resolve: model %q is inactive", modelKey)
	}

	// 2. 如果启用 thinking，查找 thinking 变体
	targetModelID := baseModel.ID
	enableThinking := false
	if thinking && baseModel.SupportsThinking {
		variant, err := r.ds.LLMModel().GetThinkingVariant(ctx, baseModel.ID)
		if err != nil {
			// thinking 变体不存在，降级到基础模型
			log.C(ctx).Warnw("Resolve: thinking variant not found, falling back to base model",
				"model_key", modelKey, "error", err)
		} else {
			targetModelID = variant.ID
			enableThinking = true
		}
	}

	// 3. 查缓存
	if cached, ok := r.cache.getRoutes(targetModelID); ok {
		return cached, nil
	}

	// 4. 查 DB: 路由列表（Preload Provider）
	routes, err := r.ds.LLMModelProvider().ListActiveByModel(ctx, targetModelID)
	if err != nil {
		return nil, fmt.Errorf("Resolve: list routes for model %d: %w", targetModelID, err)
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("Resolve: no active routes for model %q", modelKey)
	}

	// 5. 转换为 ResolvedRoute
	var result []ResolvedRoute
	for _, route := range routes {
		result = append(result, ResolvedRoute{
			BaseURL:         route.Provider.BaseURL,
			APIKey:          route.Provider.APIKey,
			ProviderModelID: route.ProviderModelID,
			ProviderName:    route.Provider.Name,
			EnableThinking:  enableThinking,
		})
	}

	// 6. 缓存
	r.cache.setRoutes(targetModelID, result)
	return result, nil
}

// StreamChat 统一流式调用入口：解析路由 + 调用 + fallback + 计费 + Langfuse
func (r *LLMRouter) StreamChat(ctx context.Context, modelKey string, thinking bool,
	messages []llm.ChatMessage, temperature float64, maxTokens int,
	onEvent func(eventType, content string) error) (string, *billing.TokenUsage, error) {

	routes, err := r.Resolve(ctx, modelKey, thinking)
	if err != nil {
		return "", nil, fmt.Errorf("StreamChat: %w", err)
	}

	var lastErr error
	for i, route := range routes {
		client := llm.NewDMXAPIClientWithConfig(route.BaseURL, route.APIKey)

		// 标记来自 LLMRouter，跳过 DMXAPIClient 内部的 Langfuse/billing
		routerCtx := llm.WithLLMRouterMark(ctx)

		content, usage, err := client.StreamChatCompletion(
			routerCtx, route.ProviderModelID, messages,
			temperature, maxTokens, route.EnableThinking, onEvent,
		)

		if err != nil {
			log.C(ctx).Warnw("StreamChat: provider failed, trying next",
				"provider", route.ProviderName,
				"model", route.ProviderModelID,
				"attempt", i+1,
				"error", err)
			lastErr = err
			continue
		}

		// 成功：记录 Langfuse generation
		if tc := langfuse.FromContext(ctx); tc != nil {
			genID := langfuse.SpanID()
			genOpts := []langfuse.GenOption{
				langfuse.WithGenParent(tc.ParentObservationID),
				langfuse.WithGenName("llm-chat"),
				langfuse.WithGenModel(route.ProviderModelID),
				langfuse.WithGenInput(messages),
				langfuse.WithGenOutput(content),
				langfuse.WithGenMetadata(map[string]interface{}{
					"provider":      route.ProviderName,
					"logical_model": modelKey,
					"thinking":      thinking,
				}),
			}
			langfuse.CreateGeneration(tc.TraceID, genID, genOpts...)
			var endOpts []langfuse.GenOption
			if usage != nil {
				endOpts = append(endOpts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
			}
			langfuse.EndGeneration(genID, endOpts...)
		}

		// 成功：记录计费（使用实际 provider + model）
		if bc := billing.FromContext(ctx); bc != nil && usage != nil {
			billing.RecordLLM(bc.UserID, route.ProviderName, route.ProviderModelID, bc.Operation, usage, bc.Meta)
		}

		return content, usage, nil
	}

	return "", nil, fmt.Errorf("StreamChat: all %d providers failed for model %q: %w", len(routes), modelKey, lastErr)
}
```

- [ ] **Step 4: Verify compilation**

```bash
cd numind-server && go build ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/numind/biz/llmrouter/
git commit -m "feat: add LLMRouter service layer with Resolve, StreamChat, and cache"
```

---

### Task 5: User-Facing LLM API (Models + Preference)

**Files:**
- Create: `numind-server/internal/numind/biz/llmrouter/preference.go`
- Create: `numind-server/internal/numind/controller/v1/llm/model.go`
- Modify: `numind-server/internal/numind/biz/biz.go` — register LLMRouter
- Modify: `numind-server/internal/numind/router.go` — register user-facing LLM routes

- [ ] **Step 1: Add preference biz methods to LLMRouter**

Create `numind-server/internal/numind/biz/llmrouter/preference.go`:

```go
package llmrouter

import (
	"context"
	"errors"
	"fmt"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// PreferenceResult 用户偏好查询结果
type PreferenceResult struct {
	ModelKey string `json:"model_key"`
	Thinking bool   `json:"thinking"`
}

// GetModels 返回用户端可用的基础模型列表
func (r *LLMRouter) GetModels(ctx context.Context) ([]model.LLMModel, string, error) {
	if cached, ok := r.cache.getModels(); ok {
		defaultKey := ""
		if len(cached) > 0 {
			defaultKey = cached[0].ModelKey
		}
		return cached, defaultKey, nil
	}

	models, err := r.ds.LLMModel().ListActiveBase(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("GetModels: %w", err)
	}
	r.cache.setModels(models)

	defaultKey := ""
	if len(models) > 0 {
		defaultKey = models[0].ModelKey
	}
	return models, defaultKey, nil
}

// GetPreferences 返回用户全部偏好（chatbot + sop）
func (r *LLMRouter) GetPreferences(ctx context.Context, userID uint64) (map[string]PreferenceResult, error) {
	prefs, err := r.ds.UserModelPreference().GetAll(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("GetPreferences: %w", err)
	}

	result := make(map[string]PreferenceResult)
	for _, p := range prefs {
		result[p.Feature] = PreferenceResult{
			ModelKey: p.ModelKey,
			Thinking: p.Thinking,
		}
	}

	// 填充默认值
	for _, feature := range []string{"chatbot", "sop"} {
		if _, ok := result[feature]; !ok {
			defaultModel, err := r.ds.LLMModel().GetDefaultModel(ctx)
			if err == nil {
				result[feature] = PreferenceResult{ModelKey: defaultModel.ModelKey, Thinking: false}
			}
		}
	}

	return result, nil
}

// SavePreference 保存用户模型偏好
func (r *LLMRouter) SavePreference(ctx context.Context, userID uint64, feature, modelKey string, thinking bool) error {
	// 校验 feature
	if feature != "chatbot" && feature != "sop" {
		return fmt.Errorf("SavePreference: invalid feature %q", feature)
	}

	// 校验 model_key 存在且活跃且为基础模型
	m, err := r.ds.LLMModel().GetByKey(ctx, modelKey)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("SavePreference: model %q not found", modelKey)
		}
		return fmt.Errorf("SavePreference: %w", err)
	}
	if !m.IsActive || m.IsThinking {
		return fmt.Errorf("SavePreference: model %q is not a valid base model", modelKey)
	}

	// 校验 thinking
	if thinking && !m.SupportsThinking {
		return fmt.Errorf("SavePreference: model %q does not support thinking", modelKey)
	}

	return r.ds.UserModelPreference().Upsert(ctx, &model.UserModelPreference{
		UserID:   userID,
		Feature:  feature,
		ModelKey:  modelKey,
		Thinking: thinking,
	})
}

// ResolveUserModel 三级回退逻辑：query 参数 → 用户偏好 → 系统默认
func (r *LLMRouter) ResolveUserModel(ctx context.Context, userID uint64, feature, queryModelKey string, queryThinking *bool) (string, bool, error) {
	// 1. query 参数优先
	if queryModelKey != "" {
		thinking := false
		if queryThinking != nil {
			thinking = *queryThinking
		}
		return queryModelKey, thinking, nil
	}

	// 2. 用户偏好
	pref, err := r.ds.UserModelPreference().Get(ctx, userID, feature)
	if err == nil {
		return pref.ModelKey, pref.Thinking, nil
	}

	// 3. 系统默认
	defaultModel, err := r.ds.LLMModel().GetDefaultModel(ctx)
	if err != nil {
		return "", false, fmt.Errorf("ResolveUserModel: no default model available: %w", err)
	}
	return defaultModel.ModelKey, false, nil
}
```

- [ ] **Step 2: Create user-facing LLM controller**

Create `numind-server/internal/numind/controller/v1/llm/model.go`:

```go
package llm

import (
	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// LLMController 用户端 LLM 模型控制器
type LLMController struct {
	router *llmrouter.LLMRouter
}

// NewLLMController 创建用户端 LLM 控制器
func NewLLMController(router *llmrouter.LLMRouter) *LLMController {
	return &LLMController{router: router}
}

// ListModels GET /v1/llm/models — 返回可用基础模型列表
func (ctrl *LLMController) ListModels(c *gin.Context) {
	models, defaultKey, err := ctrl.router.GetModels(c.Request.Context())
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"list":              models,
		"default_model_key": defaultKey,
	})
}

// GetPreference GET /v1/llm/preference — 返回当前用户模型偏好
func (ctrl *LLMController) GetPreference(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	prefs, err := ctrl.router.GetPreferences(c.Request.Context(), uint64(user.ID))
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage(err.Error()), nil)
		return
	}
	core.WriteResponse(c, nil, prefs)
}

type savePreferenceReq struct {
	Feature  string `json:"feature" binding:"required"`
	ModelKey string `json:"model_key" binding:"required"`
	Thinking bool   `json:"thinking"`
}

// SavePreference PUT /v1/llm/preference — 保存用户模型偏好
func (ctrl *LLMController) SavePreference(c *gin.Context) {
	var req savePreferenceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if err := ctrl.router.SavePreference(c.Request.Context(), uint64(user.ID), req.Feature, req.ModelKey, req.Thinking); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParam.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
```

- [ ] **Step 3: Register LLMRouter in biz.go**

Modify `numind-server/internal/numind/biz/biz.go`:

Add to `IBiz` interface:
```go
LLMRouter() *llmrouter.LLMRouter
```

Add field to `biz` struct:
```go
llmRouter *llmrouter.LLMRouter
```

In `NewBiz` constructor, add initialization:
```go
b.llmRouter = llmrouter.NewLLMRouter(ds)
```

Add getter:
```go
func (b *biz) LLMRouter() *llmrouter.LLMRouter {
	return b.llmRouter
}
```

- [ ] **Step 4: Register user-facing routes in router.go**

Modify `numind-server/internal/numind/router.go`:

Add controller creation after other controllers:
```go
llmCtrl := llmcontroller.NewLLMController(b.LLMRouter())
```

Add route group in the auth-protected section:
```go
// LLM 模型选择
llmGroup := authGroup.Group("/llm")
{
    llmGroup.GET("/models", llmCtrl.ListModels)
    llmGroup.GET("/preference", llmCtrl.GetPreference)
    llmGroup.PUT("/preference", llmCtrl.SavePreference)
}
```

- [ ] **Step 5: Run lint and verify**

```bash
cd numind-server && task lint && go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/llmrouter/preference.go internal/numind/controller/v1/llm/ \
  internal/numind/biz/biz.go internal/numind/router.go
git commit -m "feat: add user-facing LLM API (models list, preference CRUD)"
```

---

### Task 6: Chatbot Integration

**Files:**
- Modify: `numind-server/internal/numind/biz/chatbot/stream.go`
- Modify: `numind-server/internal/numind/biz/chatbot/chatbot.go` (biz interface + constructor)
- Modify: `numind-server/internal/numind/controller/v1/chatbot/chatbot.go`

- [ ] **Step 1: Add llmRouter dependency to chatbot biz**

Modify `internal/numind/biz/chatbot/chatbot.go` (or wherever chatbotBiz is defined):

Add `llmRouter *llmrouter.LLMRouter` field and accept it in the constructor. Add `llmRouter` as a parameter to `NewChatbotBiz(...)`.

- [ ] **Step 2: Modify ChatStream to accept modelKey + thinking**

Modify `internal/numind/biz/chatbot/stream.go`:

Change `ChatStream` signature to include model parameters:
```go
func (b *chatbotBiz) ChatStream(ctx context.Context, userID uint, sessionID uint, message string, modelKey string, thinking bool, handler StreamHandler) error {
```

Replace the LLM call section (around line 174):

```go
// 之前：
// result, usage, llmErr := b.volcBiz.StreamChatWithModel(ctx, messages, chatStreamDefaultModel, 0, 0.7, "minimal", ...)

// 之后：通过 LLMRouter 调用
// 转换 messages 到 llm.ChatMessage 格式
var chatMessages []llm.ChatMessage
for _, msg := range messages {
    role, _ := msg["role"].(string)
    content, _ := msg["content"].(string)
    chatMessages = append(chatMessages, llm.ChatMessage{Role: role, Content: content})
}

result, usage, llmErr := b.llmRouter.StreamChat(ctx, modelKey, thinking, chatMessages, 0.7, 0,
    func(eventType, content string) error {
        if eventType == "thinking" {
            thinkingContent.WriteString(content)
            return handler("thinking", map[string]string{"content": content})
        }
        fullContent.WriteString(content)
        return handler("token", map[string]string{"content": content})
    },
)
```

Remove the `chatStreamDefaultModel` constant.

Remove the manual `CreateGeneration` at line 150-153 (now handled by LLMRouter).

Keep the trace creation and context-assembly span — those are chatbot-level, not LLM-level.

- [ ] **Step 3: Modify chatbot controller to pass model params**

Modify `internal/numind/controller/v1/chatbot/chatbot.go` — in the SSE chat handler:

Read query params:
```go
modelKey := c.Query("model_key")
thinkingStr := c.Query("thinking")
thinking := thinkingStr == "1" || thinkingStr == "true"

// 三级回退
resolvedModelKey, resolvedThinking, err := ctrl.llmRouter.ResolveUserModel(
    c.Request.Context(), uint64(user.ID), "chatbot", modelKey, &thinking)
```

Pass to biz:
```go
err = ctrl.chatbotBiz.ChatStream(ctx, user.ID, sessionID, message, resolvedModelKey, resolvedThinking, handler)
```

The controller needs `llmRouter` as an additional dependency.

- [ ] **Step 4: Update biz.go to pass LLMRouter to chatbot**

Update the chatbot initialization in `biz.go` to pass `b.llmRouter`.

- [ ] **Step 5: Run lint and verify**

```bash
cd numind-server && task lint && go build ./...
```

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/chatbot/ internal/numind/controller/v1/chatbot/ internal/numind/biz/biz.go
git commit -m "feat: integrate LLMRouter into chatbot stream"
```

---

### Task 7: SOP Executor Integration

**Files:**
- Modify: `numind-server/internal/numind/biz/sop/executor.go`
- Modify: `numind-server/internal/numind/biz/sop/sop.go`
- Modify: `numind-server/internal/numind/controller/v1/sop/sop.go`

- [ ] **Step 1: Add llmRouter dependency to SopExecutor**

Modify `internal/numind/biz/sop/executor.go`:

Add `llmRouter` field to `SopExecutor` struct and update `NewSopExecutor`:

```go
type SopExecutor struct {
	ds        store.IStore
	tokenizer *tokenizer.Tokenizer
	llmRouter *llmrouter.LLMRouter
}

func NewSopExecutor(ds store.IStore, llmRouter *llmrouter.LLMRouter) *SopExecutor {
    // ... existing init code ...
    return &SopExecutor{ds: ds, tokenizer: tk, llmRouter: llmRouter}
}
```

- [ ] **Step 2: Modify ExecuteNodeStream to support LLMRouter path**

Modify `ExecuteNodeStream` (or `ExecuteNodeStreamWithThinking`) to accept model params:

```go
func (e *SopExecutor) ExecuteNodeStream(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, modelKey string, thinking bool, handler StreamHandler) (string, *TokenUsage, error) {
	// 如果用户选择了模型 → 通过 LLMRouter 调用
	if modelKey != "" && e.llmRouter != nil {
		return e.executeViaRouter(ctx, node, input, history, modelKey, thinking, handler)
	}

	// 否则保留现有逻辑
	applyDefaultLLMConfig(node)
	// ... 原有 HTTP 调用逻辑 ...
}

func (e *SopExecutor) executeViaRouter(ctx context.Context, node *model.SopNode, input string, history []LLMMessage, modelKey string, thinking bool, handler StreamHandler) (string, *TokenUsage, error) {
	// 转换 LLMMessage → llm.ChatMessage
	var messages []llm.ChatMessage

	// system prompt
	if node.Prompt != "" {
		messages = append(messages, llm.ChatMessage{Role: "system", Content: node.Prompt})
	}

	// history
	for _, msg := range history {
		messages = append(messages, llm.ChatMessage{Role: msg.Role, Content: msg.Content})
	}

	// user input
	messages = append(messages, llm.ChatMessage{Role: "user", Content: input})

	maxTokens := 4096
	if node.MaxTokens > 0 {
		maxTokens = node.MaxTokens
	}

	content, usage, err := e.llmRouter.StreamChat(ctx, modelKey, thinking, messages, 0.7, maxTokens, handler)
	return content, usage, err
}
```

- [ ] **Step 3: Modify SOP biz to pass modelKey + thinking through**

Modify `internal/numind/biz/sop/sop.go` — `ExecuteNodeStream` biz method:

Add `modelKey string, thinking bool` parameters and pass through to executor.

- [ ] **Step 4: Modify SOP controller to read model query params**

Modify `internal/numind/controller/v1/sop/sop.go` — `ExecuteNodeStream` handler:

```go
modelKey := c.Query("model_key")
thinkingStr := c.Query("thinking")
thinking := thinkingStr == "1" || thinkingStr == "true"

// 三级回退
resolvedModelKey, resolvedThinking, err := ctrl.llmRouter.ResolveUserModel(
    c.Request.Context(), uint64(userID), "sop", modelKey, &thinking)
```

Pass to biz call.

- [ ] **Step 5: Update biz.go to pass LLMRouter to SOP**

Update SOP biz initialization in `biz.go`.

- [ ] **Step 6: Run lint and verify**

```bash
cd numind-server && task lint && go build ./...
```

- [ ] **Step 7: Commit**

```bash
git add internal/numind/biz/sop/ internal/numind/controller/v1/sop/ internal/numind/biz/biz.go
git commit -m "feat: integrate LLMRouter into SOP executor"
```

---

### Task 8a: Admin API — Provider CRUD

**Files:**
- Create: `numind-server/internal/numind/controller/v1/admin_llm/admin_llm.go`
- Modify: `numind-server/internal/numind/admin_router.go`

- [ ] **Step 1: Create admin LLM controller with Provider CRUD**

Create `numind-server/internal/numind/controller/v1/admin_llm/admin_llm.go`:

```go
package admin_llm

import (
	"strconv"

	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

// AdminLLMController 管理端 LLM 控制器
type AdminLLMController struct {
	router *llmrouter.LLMRouter
}

// NewAdminLLMController 创建管理端 LLM 控制器
func NewAdminLLMController(router *llmrouter.LLMRouter) *AdminLLMController {
	return &AdminLLMController{router: router}
}

// ListProviders GET /v1/admin/llm/providers
func (ctrl *AdminLLMController) ListProviders(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	offset := (page - 1) * pageSize

	items, total, err := ctrl.router.ListProviders(c.Request.Context(), offset, pageSize)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage(err.Error()), nil)
		return
	}

	// API key 脱敏
	type providerResp struct {
		model.LLMProvider
		APIKeyMasked string `json:"api_key_masked"`
	}
	var resp []providerResp
	for _, p := range items {
		resp = append(resp, providerResp{LLMProvider: p, APIKeyMasked: p.MaskedAPIKey()})
	}

	core.WriteResponse(c, nil, gin.H{"list": resp, "total": total})
}

type createProviderReq struct {
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"display_name" binding:"required"`
	BaseURL     string `json:"base_url" binding:"required"`
	APIKey      string `json:"api_key" binding:"required"`
}

// CreateProvider POST /v1/admin/llm/providers
func (ctrl *AdminLLMController) CreateProvider(c *gin.Context) {
	var req createProviderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil)
		return
	}
	p := &model.LLMProvider{
		Name: req.Name, DisplayName: req.DisplayName,
		BaseURL: req.BaseURL, APIKey: req.APIKey, IsActive: true,
	}
	if err := ctrl.router.CreateProvider(c.Request.Context(), p); err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage(err.Error()), nil)
		return
	}
	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, p)
}

type updateProviderReq struct {
	DisplayName string `json:"display_name"`
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	IsActive    *bool  `json:"is_active"`
}

// UpdateProvider PUT /v1/admin/llm/providers/:id
func (ctrl *AdminLLMController) UpdateProvider(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParam.SetMessage("invalid id"), nil)
		return
	}
	var req updateProviderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage(err.Error()), nil)
		return
	}
	if err := ctrl.router.UpdateProvider(c.Request.Context(), id, req.DisplayName, req.BaseURL, req.APIKey, req.IsActive); err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage(err.Error()), nil)
		return
	}
	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, nil)
}

// DeleteProvider DELETE /v1/admin/llm/providers/:id
func (ctrl *AdminLLMController) DeleteProvider(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParam.SetMessage("invalid id"), nil)
		return
	}
	if err := ctrl.router.DeleteProvider(c.Request.Context(), id); err != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage(err.Error()), nil)
		return
	}
	ctrl.router.InvalidateCache()
	core.WriteResponse(c, nil, nil)
}
```

Note: `ListProviders`, `CreateProvider`, `UpdateProvider`, `DeleteProvider` methods need to be added to `LLMRouter` in `preference.go` (or a new `admin.go` file under `llmrouter/`). These are thin wrappers over the store layer.

- [ ] **Step 2: Register provider admin routes**

Modify `numind-server/internal/numind/admin_router.go` — add after existing admin routes:

```go
adminLLMCtrl := adminllm.NewAdminLLMController(b.LLMRouter())

llmGroup := adminGroup.Group("/llm")
{
    llmGroup.GET("/providers", adminLLMCtrl.ListProviders)
    llmGroup.POST("/providers", adminLLMCtrl.CreateProvider)
    llmGroup.PUT("/providers/:id", adminLLMCtrl.UpdateProvider)
    llmGroup.DELETE("/providers/:id", adminLLMCtrl.DeleteProvider)
}
```

- [ ] **Step 3: Run lint and verify**

```bash
cd numind-server && task lint && go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/numind/controller/v1/admin_llm/ internal/numind/admin_router.go internal/numind/biz/llmrouter/
git commit -m "feat: add admin provider CRUD API"
```

---

### Task 8b: Admin API — Model CRUD

**Files:**
- Modify: `numind-server/internal/numind/controller/v1/admin_llm/admin_llm.go`
- Modify: `numind-server/internal/numind/admin_router.go`

- [ ] **Step 1: Add Model CRUD handlers to admin controller**

Add `ListModels`, `CreateModel`, `UpdateModel`, `DeleteModel` handlers to `admin_llm.go`. Pattern identical to Provider CRUD:
- `ListModels`: paginated, returns all models (including thinking variants), sorted by sort_order
- `CreateModel`: accepts model_key, display_name, is_thinking, base_model_id, supports_thinking, icon, sort_order
- `UpdateModel`: partial update by ID
- `DeleteModel`: delete by ID
- All write operations call `ctrl.router.InvalidateCache()`

Add corresponding biz methods to LLMRouter (thin store wrappers).

- [ ] **Step 2: Register model admin routes**

```go
llmGroup.GET("/models", adminLLMCtrl.ListModels)
llmGroup.POST("/models", adminLLMCtrl.CreateModel)
llmGroup.PUT("/models/:id", adminLLMCtrl.UpdateModel)
llmGroup.DELETE("/models/:id", adminLLMCtrl.DeleteModel)
```

- [ ] **Step 3: Run lint and verify**

```bash
cd numind-server && task lint && go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/numind/controller/v1/admin_llm/ internal/numind/admin_router.go internal/numind/biz/llmrouter/
git commit -m "feat: add admin model CRUD API"
```

---

### Task 8c: Admin API — Route CRUD

**Files:**
- Modify: `numind-server/internal/numind/controller/v1/admin_llm/admin_llm.go`
- Modify: `numind-server/internal/numind/admin_router.go`

- [ ] **Step 1: Add Route CRUD handlers to admin controller**

Add `ListRoutes`, `CreateRoute`, `UpdateRoute`, `DeleteRoute` handlers:
- `ListRoutes`: receives `:modelId` from URL, returns routes for that model with Preloaded Provider
- `CreateRoute`: validates model exists, accepts provider_id, provider_model_id, priority, prices
- `UpdateRoute`: receives `:modelId` + `:routeId`, validates both exist
- `DeleteRoute`: receives `:modelId` + `:routeId`
- All write operations call `ctrl.router.InvalidateCache()`

- [ ] **Step 2: Register route admin routes**

```go
llmGroup.GET("/models/:modelId/routes", adminLLMCtrl.ListRoutes)
llmGroup.POST("/models/:modelId/routes", adminLLMCtrl.CreateRoute)
llmGroup.PUT("/models/:modelId/routes/:routeId", adminLLMCtrl.UpdateRoute)
llmGroup.DELETE("/models/:modelId/routes/:routeId", adminLLMCtrl.DeleteRoute)
```

- [ ] **Step 3: Run lint and verify**

```bash
cd numind-server && task lint && go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/numind/controller/v1/admin_llm/ internal/numind/admin_router.go
git commit -m "feat: add admin route CRUD API"
```

---

### Task 9: Backend Final Verification

**Files:** None (verification only)

- [ ] **Step 1: Run full lint**

```bash
cd numind-server && task lint
```

Expected: exit code 0

- [ ] **Step 2: Run tests**

```bash
cd numind-server && go test ./...
```

Expected: pass (excluding pre-existing salesrag failures)

- [ ] **Step 3: Verify API endpoints with curl**

Start local server and test:
```bash
# 模型列表
curl -H "Authorization: Bearer $TOKEN" http://localhost:9091/v1/llm/models

# 偏好读取
curl -H "Authorization: Bearer $TOKEN" http://localhost:9091/v1/llm/preference

# 偏好保存
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"feature":"chatbot","model_key":"deepseek-v3.2","thinking":false}' \
  http://localhost:9091/v1/llm/preference
```

---

### Task 10: Frontend API Layer + Pinia Store (numind-web-v3)

**Files:**
- Create: `numind-web-v3/src/api/llm.ts`
- Create: `numind-web-v3/src/stores/llmModel.ts`

- [ ] **Step 1: Create API layer**

Create `numind-web-v3/src/api/llm.ts`:

```typescript
import request from './request'

export interface LLMModel {
  model_key: string
  display_name: string
  supports_thinking: boolean
  icon: string
  sort_order: number
}

export interface UserPreference {
  model_key: string
  thinking: boolean
}

export interface ModelsResponse {
  list: LLMModel[]
  default_model_key: string
}

export function getModelsApi() {
  return request.get<{ data: ModelsResponse }>('/v1/llm/models')
}

export function getPreferenceApi() {
  return request.get<{ data: Record<string, UserPreference> }>('/v1/llm/preference')
}

export function savePreferenceApi(feature: string, modelKey: string, thinking: boolean) {
  return request.put('/v1/llm/preference', { feature, model_key: modelKey, thinking })
}
```

- [ ] **Step 2: Create Pinia store**

Create `numind-web-v3/src/stores/llmModel.ts`:

```typescript
import { ref, computed } from 'vue'
import { defineStore } from 'pinia'
import { getModelsApi, getPreferenceApi, savePreferenceApi, type LLMModel, type UserPreference } from '@/api/llm'

export const useLLMModelStore = defineStore('llmModel', () => {
  const models = ref<LLMModel[]>([])
  const defaultModelKey = ref('')
  const preferences = ref<Record<string, UserPreference>>({})
  const loading = ref(false)

  async function fetchModels() {
    loading.value = true
    try {
      const res = await getModelsApi()
      models.value = res.data.data.list
      defaultModelKey.value = res.data.data.default_model_key
    } finally {
      loading.value = false
    }
  }

  async function fetchPreferences() {
    try {
      const res = await getPreferenceApi()
      preferences.value = res.data.data
    } catch {
      // 静默失败，使用默认值
    }
  }

  async function savePreference(feature: string, modelKey: string, thinking: boolean) {
    await savePreferenceApi(feature, modelKey, thinking)
    preferences.value[feature] = { model_key: modelKey, thinking }
  }

  function getSelectedModel(feature: string): LLMModel | undefined {
    const pref = preferences.value[feature]
    const key = pref?.model_key || defaultModelKey.value
    return models.value.find(m => m.model_key === key)
  }

  function getSelectedModelKey(feature: string): string {
    return preferences.value[feature]?.model_key || defaultModelKey.value
  }

  function isThinkingEnabled(feature: string): boolean {
    return preferences.value[feature]?.thinking ?? false
  }

  return {
    models, defaultModelKey, preferences, loading,
    fetchModels, fetchPreferences, savePreference,
    getSelectedModel, getSelectedModelKey, isThinkingEnabled,
  }
})
```

- [ ] **Step 3: Verify lint + type-check**

```bash
cd numind-web-v3 && npm run lint && npm run type-check
```

- [ ] **Step 4: Commit**

```bash
git add src/api/llm.ts src/stores/llmModel.ts
git commit -m "feat: add LLM model API layer and Pinia store"
```

---

### Task 11: ModelSelector Component (numind-web-v3)

**Files:**
- Create: `numind-web-v3/src/components/common/ModelSelector.vue`

- [ ] **Step 1: Create ModelSelector component**

Create `numind-web-v3/src/components/common/ModelSelector.vue`:

Vue 3 `<script setup lang="ts">` component with:
- **Props**: `feature: 'chatbot' | 'sop'`
- **Template**: pill button showing current model display_name + ChevronDown icon, dropdown list of 4 models, separate "深度思考" toggle button with Brain icon
- **Logic**:
  - On mount: call `store.fetchModels()` and `store.fetchPreferences()` (if not already loaded)
  - Model selection: update store + call `savePreference`
  - Thinking toggle: update store + call `savePreference`
  - Click outside: close dropdown
  - `supports_thinking=false` → thinking toggle disabled
- **Styling**: `<style scoped>`, pill shape, dropdown popover, active model highlighted, consistent with existing UI patterns (reference `SalesStageDropdown.vue` for pill/dropdown pattern)

- [ ] **Step 2: Verify lint + type-check**

```bash
cd numind-web-v3 && npm run lint && npm run type-check
```

- [ ] **Step 3: Commit**

```bash
git add src/components/common/ModelSelector.vue
git commit -m "feat: add ModelSelector component (pill dropdown + thinking toggle)"
```

---

### Task 12: Chatbot + SOP Frontend Integration (numind-web-v3)

**Files:**
- Modify: `numind-web-v3/src/views/chatbot/ChatbotChat.vue`
- Modify: `numind-web-v3/src/stores/chatbot.ts`
- Modify: `numind-web-v3/src/views/SOPView.vue`
- Modify: `numind-web-v3/public/legacy/sop-legacy.js`

- [ ] **Step 1: Integrate ModelSelector into ChatbotChat.vue**

Add `<ModelSelector feature="chatbot" />` in the input toolbar area (before the send button).

Import and use:
```vue
<script setup lang="ts">
import ModelSelector from '@/components/common/ModelSelector.vue'
</script>
```

- [ ] **Step 2: Modify chatbot store sendMessage to include model params**

Modify `numind-web-v3/src/stores/chatbot.ts` — in the `sendMessage` function:

Read from llmModel store and append to SSE URL:
```typescript
import { useLLMModelStore } from './llmModel'

// in sendMessage:
const llmStore = useLLMModelStore()
const modelKey = llmStore.getSelectedModelKey('chatbot')
const thinking = llmStore.isThinkingEnabled('chatbot')
const url = `/v1/chatbot/sessions/${sessionId}/chat?model_key=${encodeURIComponent(modelKey)}&thinking=${thinking ? '1' : '0'}`
```

- [ ] **Step 3: Integrate ModelSelector into SOPView.vue**

Add `<ModelSelector feature="sop" />` above the SOP container.

Add a `watch` to sync model selection to `window.__selectedModel`:
```typescript
import { watch } from 'vue'
import { useLLMModelStore } from '@/stores/llmModel'
import ModelSelector from '@/components/common/ModelSelector.vue'

const llmStore = useLLMModelStore()

watch(
  () => ({
    modelKey: llmStore.getSelectedModelKey('sop'),
    thinking: llmStore.isThinkingEnabled('sop'),
  }),
  (val) => {
    ;(window as any).__selectedModel = val
  },
  { immediate: true }
)
```

- [ ] **Step 4: Modify sop-legacy.js to read model selection**

Find the `fetch` call for node execution (around line 2515-2522) and modify the URL:

```javascript
// 读取用户选择的模型
const selectedModel = window.__selectedModel || {};
let executeUrl = `${API_BASE_URL}/v1/sop/runs/${currentRunId}/nodes/${nodeId}/execute`;
if (selectedModel.modelKey) {
    executeUrl += `?model_key=${encodeURIComponent(selectedModel.modelKey)}&thinking=${selectedModel.thinking ? '1' : '0'}`;
}
```

- [ ] **Step 5: Verify lint + type-check**

```bash
cd numind-web-v3 && npm run lint && npm run type-check
```

- [ ] **Step 6: Commit**

```bash
git add src/views/chatbot/ChatbotChat.vue src/stores/chatbot.ts \
  src/views/SOPView.vue public/legacy/sop-legacy.js
git commit -m "feat: integrate ModelSelector into chatbot and SOP views"
```

---

### Task 13: Admin Provider Management Page (numind-admin-web)

**Files:**
- Create: `numind-admin-web/src/api/llm.ts`
- Create: `numind-admin-web/src/views/LLMProvidersView.vue`
- Modify: `numind-admin-web/src/router/index.ts`
- Modify: `numind-admin-web/src/components/layout/AdminSidebar.vue`

- [ ] **Step 1: Create admin LLM API layer**

Create `numind-admin-web/src/api/llm.ts` with typed functions for all admin LLM endpoints (providers, models, routes CRUD).

- [ ] **Step 2: Create LLMProvidersView.vue**

Table layout using DataTable component (following existing pattern from PricingRulesView.vue):
- Columns: 名称, 显示名称, API 地址, API Key (脱敏), 状态, 操作
- Create/edit modal with form fields
- Delete with ConfirmModal
- StatusBadge for is_active

- [ ] **Step 3: Add route and sidebar entry**

Add route in `router/index.ts` and sidebar nav item in `AdminSidebar.vue`.

- [ ] **Step 4: Verify lint + type-check**

```bash
cd numind-admin-web && npm run lint && npm run type-check
```

- [ ] **Step 5: Commit**

```bash
git add src/api/llm.ts src/views/LLMProvidersView.vue src/router/index.ts \
  src/components/layout/AdminSidebar.vue
git commit -m "feat: add admin LLM provider management page"
```

---

### Task 14: Admin Model + Route Management Page (numind-admin-web)

**Files:**
- Create: `numind-admin-web/src/views/LLMModelsView.vue`
- Modify: `numind-admin-web/src/router/index.ts`
- Modify: `numind-admin-web/src/components/layout/AdminSidebar.vue`

- [ ] **Step 1: Create LLMModelsView.vue**

Table layout with expandable rows:
- Main table: model_key, display_name, is_thinking, supports_thinking, sort_order, is_active
- Expandable row: routes sub-table showing provider, provider_model_id, priority, prices, is_active
- Create/edit model modal
- Create/edit route modal (within model context)
- Delete confirmations for both

- [ ] **Step 2: Add route and sidebar entry**

Add route and sidebar nav item.

- [ ] **Step 3: Verify lint + type-check**

```bash
cd numind-admin-web && npm run lint && npm run type-check
```

- [ ] **Step 4: Commit**

```bash
git add src/views/LLMModelsView.vue src/router/index.ts \
  src/components/layout/AdminSidebar.vue
git commit -m "feat: add admin LLM model and route management page"
```

---

### Task 15: S5 验证策略

**验证方式**: gstack `/qa` 浏览器 QA + 后端 TDD

**理由**:
- 本功能涉及两处 UI 交互（模型选择器 + 深度思考按钮）和管理端两个页面，需要浏览器可视化验证
- 后端 LLMRouter 路由逻辑需要单元测试覆盖
- 不需要 Playwright E2E：功能不涉及支付/权限等高风险业务逻辑，gstack `/qa` 的一次性验证足够

**关键用户路径：**

1. **智能体模型切换**：打开智能体对话 → 点击模型选择器 → 切换到 Claude → 发送消息 → 验证回复来自 Claude → 刷新页面 → 验证模型偏好保留
2. **智能体深度思考**：选择支持 thinking 的模型 → 开启深度思考 → 发送消息 → 验证出现思考过程
3. **SOP 模型切换**：打开 SOP → 选择模型 → 执行 SOP 节点 → 验证使用选择的模型
4. **管理端供应商管理**：创建供应商 → 编辑 → 验证 API key 脱敏显示 → 禁用 → 删除
5. **管理端模型管理**：查看模型列表 → 展开路由 → 创建路由 → 修改优先级 → 禁用路由
6. **Fallback 验证**：禁用某模型的首选供应商 → 发送消息 → 验证自动切换到次选供应商
7. **边界情况**：删除用户偏好中的模型 → 验证自动回退到默认模型
