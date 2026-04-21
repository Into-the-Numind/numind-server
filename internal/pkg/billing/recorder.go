package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// UsageStore 定义计费存储接口（避免与 store 包循环引用）
type UsageStore interface {
	CreateUsageRecord(ctx context.Context, record *model.UsageRecord) error
	CreateUsageRecords(ctx context.Context, records []*model.UsageRecord) error
	GetPricingRule(ctx context.Context, serviceType, provider, modelName string) (*model.PricingRule, error)
	GetPricingRuleTiers(ctx context.Context, ruleID uint) ([]model.PricingRuleTier, error)
	// GetProviderModelID resolves the provider-specific model ID for the given
	// logical model key and provider name. It joins ai_service and
	// ai_service_route (via llm_provider.name) to retrieve provider_model_id.
	// Returns ("", gorm.ErrRecordNotFound) when no mapping exists.
	GetProviderModelID(ctx context.Context, modelKey, providerName string) (string, error)
}

// pricingCacheEntry caches a resolved PricingRule with a 5-minute TTL.
type pricingCacheEntry struct {
	rule      *model.PricingRule
	expiresAt time.Time
}

// pricingCache is a thread-safe in-process cache for pricing rule lookups.
// Key format: "<serviceType>|<provider>|<resolvedModel>".
var pricingCache sync.Map // map[string]pricingCacheEntry

const pricingCacheTTL = 5 * time.Minute

// ResolvePricingRule looks up the pricing rule for a given (serviceType, provider,
// modelKey) triple, applying a two-step fallback when the first lookup misses:
//
//  1. Direct lookup by (serviceType, provider, modelKey).
//  2. If gorm.ErrRecordNotFound: resolve provider_model_id via GetProviderModelID
//     and retry with that ID.
//
// Results (including successful lookups) are cached for 5 minutes keyed by the
// lookup string that produced a hit, so both paths share the same cache slot.
//
// Returns (nil, gorm.ErrRecordNotFound) when neither path finds a rule.
// This function is exported so that the billing middleware (T-arch) can call it
// when building the pricing snapshot on the hot path.
func ResolvePricingRule(ctx context.Context, store UsageStore, serviceType, provider, modelKey string) (*model.PricingRule, error) {
	// --- Step 1: direct lookup with cache check ---
	directKey := serviceType + "|" + provider + "|" + modelKey
	if entry, ok := pricingCache.Load(directKey); ok {
		e := entry.(pricingCacheEntry)
		if time.Now().Before(e.expiresAt) {
			return e.rule, nil
		}
		pricingCache.Delete(directKey)
	}

	rule, err := store.GetPricingRule(ctx, serviceType, provider, modelKey)
	if err == nil {
		// Cache the hit under the direct key.
		pricingCache.Store(directKey, pricingCacheEntry{rule: rule, expiresAt: time.Now().Add(pricingCacheTTL)})
		return rule, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		// Unexpected DB error — don't cache, surface to caller.
		return nil, err
	}

	// --- Step 2: resolve provider_model_id and retry ---
	providerModelID, resolveErr := store.GetProviderModelID(ctx, modelKey, provider)
	if resolveErr != nil {
		if errors.Is(resolveErr, gorm.ErrRecordNotFound) {
			// No mapping exists — truly not found.
			return nil, gorm.ErrRecordNotFound
		}
		// Real DB error (network failure, context cancelled, etc.) — propagate so
		// the caller can retry or alert instead of silently billing at ¥0.
		return nil, resolveErr
	}
	if providerModelID == modelKey {
		// Avoid an identical second lookup that would also miss.
		return nil, gorm.ErrRecordNotFound
	}

	fallbackKey := serviceType + "|" + provider + "|" + providerModelID
	if entry, ok := pricingCache.Load(fallbackKey); ok {
		e := entry.(pricingCacheEntry)
		if time.Now().Before(e.expiresAt) {
			return e.rule, nil
		}
		pricingCache.Delete(fallbackKey)
	}

	rule, err = store.GetPricingRule(ctx, serviceType, provider, providerModelID)
	if err == nil {
		// Cache under the fallback key so subsequent calls with the same
		// provider_model_id also hit cache immediately.
		pricingCache.Store(fallbackKey, pricingCacheEntry{rule: rule, expiresAt: time.Now().Add(pricingCacheTTL)})
	}
	return rule, err
}

// UsageRecorder 用量记录器 — 异步批量写入，不阻塞主流程
//
// calc 字段是 Track B 引入的 cost 计算入口（pricing.ICalculator）：recorder
// 的 LLM 路径不再内联 cost 公式，改调 calc.CalculateCost 以保证与 biz/sop
// 同步扣减路径（spec §3.0）共用单一数据源。revenue/非-LLM 路径仍在本文件内
// 计算，因为 pricing.ICalculator 只负责 cost 维度。
type UsageRecorder struct {
	store UsageStore
	calc  pricing.ICalculator
	ch    chan *UsageEvent
	done  chan struct{}
	wg    sync.WaitGroup
}

// UsageEvent 用量事件
type UsageEvent struct {
	UserID      uint
	ServiceType string            // llm_chat, llm_vision, embedding, rerank, cos_upload, file_extract, vector_db
	Provider    string            // ali, volc, dmxapi, cos, vikingdb, dashvector, bailian
	Model       string            // 模型名称
	Operation   string            // 业务扣费点: sop_node_execute, salesrag_chat 等
	Usage       *TokenUsage       // LLM/Vision 调用的 token 用量
	Embedding   *EmbeddingUsage   // Embedding 调用的 token 用量
	Bytes       int64             // COS 上传字节数
	ItemCount   int               // 向量操作条数 / Rerank 文档数
	BizRefType  string            // 关联业务对象类型
	BizRefID    uint              // 关联业务对象 ID
	IsFallback  bool              // 是否为降级调用
	Metadata    map[string]string // 额外上下文

	// Prebuilt lets callers submit an already-populated UsageRecord and
	// bypass buildRecord. When non-nil, every other field on the event is
	// ignored and the recorder only runs cost/revenue calculation (if not
	// already set) before batching the row.
	//
	// Use case: aiservice Gateway billing middleware constructs the record
	// with AI-Service-Manager-specific fields (task_id, unit, pricing
	// snapshots, is_estimated) that don't fit in the UsageEvent shape.
	// Going through this path unifies LLM + VectorDB + COS billing on the
	// same async batched pipeline.
	Prebuilt *model.UsageRecord
}

// batchSize 批量写入阈值
const batchSize = 50

// flushInterval 定时刷盘间隔
const flushInterval = 3 * time.Second

// R 全局用量记录器实例
var R *UsageRecorder

// InitRecorder 初始化全局用量记录器。内部构造 pricing.Calculator 以便
// buildRecord 的 LLM 路径直接调用同步 cost 计算（spec §3.0）。store 需满足
// pricing.PricingStore 接口（UsageStore 已经满足，见方法签名）。
func InitRecorder(store UsageStore) {
	R = &UsageRecorder{
		store: store,
		calc:  pricing.NewCalculator(store),
		ch:    make(chan *UsageEvent, 2000),
		done:  make(chan struct{}),
	}
	R.wg.Add(1)
	go R.processLoop()
	log.Infow("Billing usage recorder initialized")
}

// Stop 优雅关闭：关闭 channel 并等待所有事件处理完毕
func (r *UsageRecorder) Stop() {
	if r == nil {
		return
	}
	close(r.ch)
	r.wg.Wait()
	close(r.done)
	log.Infow("Billing usage recorder stopped")
}

// Record 异步记录用量事件（非阻塞）
func (r *UsageRecorder) Record(event *UsageEvent) {
	if r == nil || event == nil {
		return
	}
	select {
	case r.ch <- event:
	default:
		log.Warnw("Billing recorder channel full, dropping event",
			"user_id", event.UserID,
			"operation", event.Operation)
	}
}

// processLoop 后台批量处理用量事件
func (r *UsageRecorder) processLoop() {
	defer r.wg.Done()

	batch := make([]*model.UsageRecord, 0, batchSize)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-r.ch:
			if !ok {
				// channel 已关闭，flush 剩余并退出
				if len(batch) > 0 {
					r.flushBatch(batch)
				}
				return
			}
			record := r.buildRecord(event)
			batch = append(batch, record)
			if len(batch) >= batchSize {
				r.flushBatch(batch)
				batch = make([]*model.UsageRecord, 0, batchSize)
			}
		case <-ticker.C:
			if len(batch) > 0 {
				r.flushBatch(batch)
				batch = make([]*model.UsageRecord, 0, batchSize)
			}
		}
	}
}

// buildRecord 将 UsageEvent 转换为 UsageRecord。
// Prebuilt 事件跳过字段映射，仅补齐 cost/revenue 和 CreatedAt。
func (r *UsageRecorder) buildRecord(event *UsageEvent) *model.UsageRecord {
	if event.Prebuilt != nil {
		record := event.Prebuilt
		if record.CreatedAt.IsZero() {
			record.CreatedAt = time.Now()
		}
		// Defense-in-depth: usage_record.metadata is a MySQL json column that
		// rejects empty string ("Invalid JSON text: The document is empty").
		// Middleware should initialise Metadata="{}", but a future Prebuilt
		// caller may forget — sanitise here to prevent silent INSERT failures
		// (we paid for one such regression at c3d68ec on 2026-04-18).
		if record.Metadata == "" {
			record.Metadata = "{}"
		}
		// Only compute if caller didn't already. Middlewares that snapshot
		// pricing typically leave cost/revenue zeroed for the recorder to fill.
		//
		// Known limitation: a legitimately-zero cost (free-tier model, or an
		// error-path record with zero tokens) will re-trigger calculation,
		// which is idempotent but redundant. Accept the churn to keep the
		// "caller said 'please compute'" signal simple. Callers that need to
		// assert "my computed 0 is final" can set a dummy sentinel cost that
		// rounds back to 0 at the int64 conversion.
		if record.CostCents == 0 && record.RevenueCents == 0 {
			record.CostCents, record.RevenueCents = r.calculateCostAndRevenue(record)
		}
		return record
	}

	record := &model.UsageRecord{
		UserID:        event.UserID,
		ServiceType:   event.ServiceType,
		Provider:      event.Provider,
		Model:         event.Model,
		Operation:     event.Operation,
		BizRefType:    event.BizRefType,
		BizRefID:      event.BizRefID,
		IsFallback:    event.IsFallback,
		BytesUploaded: event.Bytes,
		ItemCount:     event.ItemCount,
		CreatedAt:     time.Now(),
	}

	// 填充 token 用量
	if event.Usage != nil {
		record.PromptTokens = event.Usage.PromptTokens
		record.CompletionTokens = event.Usage.CompletionTokens
		record.TotalTokens = event.Usage.TotalTokens
		record.ReasoningTokens = event.Usage.ReasoningTokens
		record.EstimatedPromptTokens = event.Usage.EstimatedPromptTokens
	}
	if event.Embedding != nil {
		record.TotalTokens = event.Embedding.TotalTokens
	}

	// 序列化 metadata
	if len(event.Metadata) > 0 {
		if metaBytes, err := json.Marshal(event.Metadata); err == nil {
			record.Metadata = string(metaBytes)
		} else {
			log.Warnw("Failed to serialize billing metadata",
				"error", err,
				"user_id", event.UserID,
				"operation", event.Operation)
			record.Metadata = "{}"
		}
	} else {
		record.Metadata = "{}"
	}

	// 计算预估成本和收入
	record.CostCents, record.RevenueCents = r.calculateCostAndRevenue(record)

	return record
}

// flushBatch 批量写入数据库
func (r *UsageRecorder) flushBatch(batch []*model.UsageRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := r.store.CreateUsageRecords(ctx, batch); err != nil {
		log.Errorw("Failed to batch save usage records",
			"error", err,
			"count", len(batch))
	}
}

// calculateCostAndRevenue 根据定价规则计算预估成本和收入（分）。
//
// 自 Track B 起：LLM（prompt+completion）路径的 cost 计算委派给
// pricing.Calculator（`r.calc.CalculateCost`），以保证 biz/sop 同步扣减路径与
// recorder 异步入库使用同一公式（spec §3.0 单一数据源）。revenue 和非-LLM
// 路径（embedding/storage/per-call）暂留在 recorder 内部，因为
// pricing.ICalculator 只面向 cost 维度；后续 track 如需统一，再在 pricing
// 包里扩展 CalculateRevenue / CalculatePerCall。
//
// cost == 0 的容错行为保持不变：pricing 返回错误时视作"无定价规则"，记录
// CostCents=0 落库即可，不阻塞 usage_record 插入。
func (r *UsageRecorder) calculateCostAndRevenue(record *model.UsageRecord) (int64, int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	costCents := r.computeCost(ctx, record)
	revenueCents := r.computeRevenue(ctx, record)

	// 如果售价未设置（为0），fallback 到成本价，保持现有行为。
	if revenueCents == 0 && costCents > 0 {
		revenueCents = costCents
	}

	return costCents, revenueCents
}

// computeCost returns the cost in cents for the given usage_record, delegating
// the LLM (prompt+completion) path to pricing.Calculator and falling back to
// the non-LLM pricing formulas (embedding / storage / per-call) for other
// service types. On any lookup error the function returns 0 — cost is
// best-effort telemetry, not load-bearing for the insert.
func (r *UsageRecorder) computeCost(ctx context.Context, record *model.UsageRecord) int64 {
	// LLM path: single source of truth via pricing.Calculator.
	if record.PromptTokens > 0 || record.CompletionTokens > 0 {
		if r.calc == nil {
			return 0 // Defensive: tests may build a UsageRecorder without wiring calc.
		}
		costCents, err := r.calc.CalculateCost(ctx, record.ServiceType, record.Provider, record.Model,
			record.PromptTokens, record.CompletionTokens)
		if err != nil {
			return 0
		}
		return costCents
	}

	// Non-LLM paths still need the raw pricing_rule for embedding /storage /
	// per-call formulas. ResolvePricingRule stays in this package (see file
	// header) and shares the LRU cache so non-LLM traffic also benefits.
	rule, err := ResolvePricingRule(ctx, r.store, record.ServiceType, record.Provider, record.Model)
	if err != nil {
		return 0
	}

	var costYuan float64
	switch {
	case record.TotalTokens > 0:
		// Embedding: only total_tokens populated, reuse input price column.
		costYuan = float64(record.TotalTokens) / 1_000_000 * rule.InputPricePerMTok
	case record.BytesUploaded > 0:
		// Storage (COS): price per GB.
		costYuan = float64(record.BytesUploaded) / (1024 * 1024 * 1024) * rule.PricePerGB
	default:
		// Flat per-call (file_extract, rerank, etc.).
		costYuan = rule.PricePerCall
	}
	return int64(math.Round(costYuan * 100))
}

// computeRevenue returns revenue in cents using the sell-price columns on the
// pricing_rule row. pricing.Calculator intentionally does not cover this
// dimension (spec §3.0 is cost-only), so the formula stays inline and shares
// the ResolvePricingRule cache with computeCost.
func (r *UsageRecorder) computeRevenue(ctx context.Context, record *model.UsageRecord) int64 {
	rule, err := ResolvePricingRule(ctx, r.store, record.ServiceType, record.Provider, record.Model)
	if err != nil {
		return 0
	}

	var revenueYuan float64
	switch {
	case record.PromptTokens > 0 || record.CompletionTokens > 0:
		if rule.BillingMode == "tiered_token" {
			revenueYuan = r.calculateTieredRevenue(ctx, rule.ID, record.PromptTokens, record.CompletionTokens)
		} else {
			revenueYuan = float64(record.PromptTokens)/1_000_000*rule.SellInputPricePerMTok +
				float64(record.CompletionTokens)/1_000_000*rule.SellOutputPricePerMTok
		}
	case record.TotalTokens > 0:
		revenueYuan = float64(record.TotalTokens) / 1_000_000 * rule.SellInputPricePerMTok
	case record.BytesUploaded > 0:
		revenueYuan = float64(record.BytesUploaded) / (1024 * 1024 * 1024) * rule.SellPricePerGB
	default:
		revenueYuan = rule.SellPricePerCall
	}
	return int64(math.Round(revenueYuan * 100))
}

// calculateTieredRevenue looks up the sell-price tier for a tiered_token rule.
// Mirrors the cost-side tier lookup in pricing.calculator.calculateTieredCost
// so operator-visible billing (revenue) stays consistent with the bracket
// selected by prompt-token count.
func (r *UsageRecorder) calculateTieredRevenue(ctx context.Context, ruleID uint, promptTokens, completionTokens int) float64 {
	tiers, err := r.store.GetPricingRuleTiers(ctx, ruleID)
	if err != nil || len(tiers) == 0 {
		return 0
	}

	lookup := func(tokenType string) float64 {
		for _, tier := range tiers {
			if tier.TokenType != tokenType {
				continue
			}
			if uint(promptTokens) >= tier.MinTokens &&
				(tier.MaxTokens == nil || uint(promptTokens) <= *tier.MaxTokens) {
				return tier.SellPerMTok
			}
		}
		return 0
	}

	inputSell := lookup("input")
	outputSell := lookup("output")
	return float64(promptTokens)/1_000_000*inputSell + float64(completionTokens)/1_000_000*outputSell
}

// RecordLLM 便捷方法：记录 LLM 调用
func RecordLLM(userID uint, provider, modelName, operation string, usage *TokenUsage, metadata map[string]string) {
	if R == nil {
		return
	}
	if usage == nil {
		log.Debugw("Billing: LLM usage is nil, skipping",
			"user_id", userID,
			"operation", operation,
			"provider", provider,
			"model", modelName)
		return
	}
	R.Record(&UsageEvent{
		UserID:      userID,
		ServiceType: "llm_chat",
		Provider:    provider,
		Model:       modelName,
		Operation:   operation,
		Usage:       usage,
		Metadata:    metadata,
	})
}

// RecordVision 便捷方法：记录 Vision 调用
func RecordVision(userID uint, provider, modelName, operation string, usage *TokenUsage, metadata map[string]string) {
	if R == nil {
		return
	}
	if usage == nil {
		log.Debugw("Billing: Vision usage is nil, skipping",
			"user_id", userID,
			"operation", operation,
			"provider", provider,
			"model", modelName)
		return
	}
	R.Record(&UsageEvent{
		UserID:      userID,
		ServiceType: "llm_vision",
		Provider:    provider,
		Model:       modelName,
		Operation:   operation,
		Usage:       usage,
		Metadata:    metadata,
	})
}

// RecordEmbedding 便捷方法：记录 Embedding 调用
func RecordEmbedding(userID uint, provider, modelName, operation string, usage *EmbeddingUsage, metadata map[string]string) {
	if R == nil {
		return
	}
	if usage == nil {
		log.Debugw("Billing: Embedding usage is nil, skipping",
			"user_id", userID,
			"operation", operation,
			"provider", provider,
			"model", modelName)
		return
	}
	R.Record(&UsageEvent{
		UserID:      userID,
		ServiceType: "embedding",
		Provider:    provider,
		Model:       modelName,
		Operation:   operation,
		Embedding:   usage,
		Metadata:    metadata,
	})
}

// RecordCOS 便捷方法：记录 COS 上传
func RecordCOS(userID uint, operation string, bytesUploaded int64, metadata map[string]string) {
	if R == nil {
		return
	}
	R.Record(&UsageEvent{
		UserID:      userID,
		ServiceType: "cos_upload",
		Provider:    "cos",
		Operation:   operation,
		Bytes:       bytesUploaded,
		Metadata:    metadata,
	})
}

// RecordAPICall 便捷方法：记录按次计费的 API 调用
func RecordAPICall(userID uint, provider, operation string, metadata map[string]string) {
	if R == nil {
		return
	}
	R.Record(&UsageEvent{
		UserID:      userID,
		ServiceType: "file_extract",
		Provider:    provider,
		Operation:   operation,
		Metadata:    metadata,
	})
}

// Metadata 快速构建 metadata map 的辅助函数
func Metadata(kvs ...string) map[string]string {
	if len(kvs)%2 != 0 {
		kvs = append(kvs, "")
	}
	m := make(map[string]string, len(kvs)/2)
	for i := 0; i < len(kvs); i += 2 {
		m[kvs[i]] = kvs[i+1]
	}
	return m
}

// FormatUint 将 uint 转为字符串（用于 metadata）
func FormatUint(v uint) string {
	return fmt.Sprintf("%d", v)
}

// RecordVectorDB 便捷方法：记录向量数据库操作（Upsert/Search）
func RecordVectorDB(userID uint, provider, operation string, itemCount int, metadata map[string]string) {
	if R == nil {
		return
	}
	R.Record(&UsageEvent{
		UserID:      userID,
		ServiceType: "vector_db",
		Provider:    provider,
		Operation:   operation,
		ItemCount:   itemCount,
		Metadata:    metadata,
	})
}

// RecordRerank 便捷方法：记录 Rerank 调用
func RecordRerank(userID uint, provider, operation string, docCount int, metadata map[string]string) {
	if R == nil {
		return
	}
	R.Record(&UsageEvent{
		UserID:      userID,
		ServiceType: "rerank",
		Provider:    provider,
		Operation:   operation,
		ItemCount:   docCount,
		Metadata:    metadata,
	})
}
