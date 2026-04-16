package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// UsageStore 定义计费存储接口（避免与 store 包循环引用）
type UsageStore interface {
	CreateUsageRecord(ctx context.Context, record *model.UsageRecord) error
	CreateUsageRecords(ctx context.Context, records []*model.UsageRecord) error
	GetPricingRule(ctx context.Context, serviceType, provider, modelName string) (*model.PricingRule, error)
	GetPricingRuleTiers(ctx context.Context, ruleID uint) ([]model.PricingRuleTier, error)
}

// UsageRecorder 用量记录器 — 异步批量写入，不阻塞主流程
type UsageRecorder struct {
	store UsageStore
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
}

// batchSize 批量写入阈值
const batchSize = 50

// flushInterval 定时刷盘间隔
const flushInterval = 3 * time.Second

// R 全局用量记录器实例
var R *UsageRecorder

// InitRecorder 初始化全局用量记录器
func InitRecorder(store UsageStore) {
	R = &UsageRecorder{
		store: store,
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

// buildRecord 将 UsageEvent 转换为 UsageRecord
func (r *UsageRecorder) buildRecord(event *UsageEvent) *model.UsageRecord {
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

// calculateCostAndRevenue 根据定价规则计算预估成本和收入（分）
func (r *UsageRecorder) calculateCostAndRevenue(record *model.UsageRecord) (int64, int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	rule, err := r.store.GetPricingRule(ctx, record.ServiceType, record.Provider, record.Model)
	if err != nil {
		return 0, 0 // 无定价规则，成本和收入为 0（不影响记录）
	}

	var costYuan, revenueYuan float64

	switch {
	case record.PromptTokens > 0 || record.CompletionTokens > 0:
		if rule.BillingMode == "tiered_token" {
			costYuan, revenueYuan = r.calculateTieredCost(ctx, rule.ID, record.PromptTokens, record.CompletionTokens)
		} else {
			costYuan = float64(record.PromptTokens)/1_000_000*rule.InputPricePerMTok +
				float64(record.CompletionTokens)/1_000_000*rule.OutputPricePerMTok
			revenueYuan = float64(record.PromptTokens)/1_000_000*rule.SellInputPricePerMTok +
				float64(record.CompletionTokens)/1_000_000*rule.SellOutputPricePerMTok
		}
	case record.TotalTokens > 0:
		// Embedding 计费: 只有 total_tokens，用 input 价格
		costYuan = float64(record.TotalTokens) / 1_000_000 * rule.InputPricePerMTok
		revenueYuan = float64(record.TotalTokens) / 1_000_000 * rule.SellInputPricePerMTok
	case record.BytesUploaded > 0:
		// 存储计费: 每 GB 价格
		costYuan = float64(record.BytesUploaded) / (1024 * 1024 * 1024) * rule.PricePerGB
		revenueYuan = float64(record.BytesUploaded) / (1024 * 1024 * 1024) * rule.SellPricePerGB
	default:
		// 按次计费
		costYuan = rule.PricePerCall
		revenueYuan = rule.SellPricePerCall
	}

	// 如果售价未设置（为0），fallback 到成本价
	if revenueYuan == 0 && costYuan > 0 {
		revenueYuan = costYuan
	}

	return int64(math.Round(costYuan * 100)), int64(math.Round(revenueYuan * 100))
}

// calculateTieredCost 从 pricing_rule_tier 子表查找匹配档位计算分段价格。
// 档位以 input token 数为索引：input 和 output 的价格都按 promptTokens 所属档位取值。
func (r *UsageRecorder) calculateTieredCost(ctx context.Context, ruleID uint, promptTokens, completionTokens int) (costYuan, revenueYuan float64) {
	tiers, err := r.store.GetPricingRuleTiers(ctx, ruleID)
	if err != nil || len(tiers) == 0 {
		return 0, 0
	}

	lookup := func(tokenType string) (cost, sell float64) {
		for _, tier := range tiers {
			if tier.TokenType != tokenType {
				continue
			}
			if uint(promptTokens) >= tier.MinTokens && (tier.MaxTokens == nil || uint(promptTokens) <= *tier.MaxTokens) {
				return tier.CostPerMTok, tier.SellPerMTok
			}
		}
		return 0, 0
	}

	inputCost, inputSell := lookup("input")
	outputCost, outputSell := lookup("output")

	costYuan = float64(promptTokens)/1_000_000*inputCost + float64(completionTokens)/1_000_000*outputCost
	revenueYuan = float64(promptTokens)/1_000_000*inputSell + float64(completionTokens)/1_000_000*outputSell
	return costYuan, revenueYuan
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
