package llmrouter

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/llm"
	"numind-server/internal/pkg/log"
)

// Router LLM 路由服务，负责模型解析、路由选择和 LLM 调用
type Router struct {
	ds    store.IStore
	cache *cache
}

// New 创建新的 LLMRouter 实例
func New(ds store.IStore) *Router {
	return &Router{
		ds:    ds,
		cache: newCache(),
	}
}

// Resolve 根据 modelKey 和 thinking 标志解析路由列表
// 优先从缓存获取；若 thinking=true 且模型支持，则查找 thinking 变体
func (r *Router) Resolve(ctx context.Context, modelKey string, thinking bool) ([]ResolvedRoute, error) {
	// 1. 获取基础模型
	baseModel, err := r.ds.LLMModel().GetByKey(ctx, modelKey)
	if err != nil {
		return nil, fmt.Errorf("llmrouter.Resolve: get model by key %q: %w", modelKey, err)
	}

	// 2. 确定实际使用的模型 ID（可能是 thinking 变体）
	targetModelID := baseModel.ID

	// thinking_only 模型始终启用思考，无需查找变体
	if !baseModel.ThinkingOnly && thinking && baseModel.SupportsThinking {
		variant, err := r.ds.LLMModel().GetThinkingVariant(ctx, baseModel.ID)
		if err != nil {
			// thinking 变体未找到，fallback 到基础模型
			log.Warnw("LLMRouter: thinking variant not found, falling back to base model",
				"model_key", modelKey,
				"base_model_id", baseModel.ID,
				"error", err,
			)
		} else {
			targetModelID = variant.ID
		}
	}

	// 3. 检查路由缓存
	if routes, ok := r.cache.getRoutes(targetModelID); ok {
		return routes, nil
	}

	// 4. 从 DB 查询路由（已 Preload Provider）
	mps, err := r.ds.LLMModelProvider().ListActiveByModel(ctx, targetModelID)
	if err != nil {
		return nil, fmt.Errorf("llmrouter.Resolve: list active routes for model %d: %w", targetModelID, err)
	}
	if len(mps) == 0 {
		return nil, fmt.Errorf("llmrouter.Resolve: no active routes for model %q (id=%d)", modelKey, targetModelID)
	}

	// 5. 转换为 ResolvedRoute 列表
	wantThinking := baseModel.ThinkingOnly || (thinking && targetModelID != baseModel.ID)
	routes := make([]ResolvedRoute, 0, len(mps))
	for _, mp := range mps {
		if mp.Provider == nil {
			continue
		}
		tf := ThinkingNone
		if wantThinking {
			tf = inferThinkingFormat(mp.ProviderModelID)
		}
		routes = append(routes, ResolvedRoute{
			BaseURL:         mp.Provider.BaseURL,
			APIKey:          mp.Provider.APIKey,
			ProviderModelID: mp.ProviderModelID,
			ProviderName:    mp.Provider.Name,
			ThinkingFormat:  tf,
		})
	}

	if len(routes) == 0 {
		return nil, fmt.Errorf("llmrouter.Resolve: no valid routes (provider nil) for model %q", modelKey)
	}

	// 6. 写入缓存并返回
	r.cache.setRoutes(targetModelID, routes)
	return routes, nil
}

// StreamChat 通过路由列表执行流式 LLM 调用，自动 failover
// 成功后记录 Langfuse generation 和 billing 用量
func (r *Router) StreamChat(
	ctx context.Context,
	modelKey string,
	thinking bool,
	messages []llm.ChatMessage,
	temperature float64,
	maxTokens int,
	onEvent func(eventType, content string) error,
) (string, *billing.TokenUsage, error) {
	// 1. 解析路由列表
	routes, err := r.Resolve(ctx, modelKey, thinking)
	if err != nil {
		return "", nil, fmt.Errorf("llmrouter.StreamChat: resolve routes: %w", err)
	}

	var lastErr error

	// 2. 遍历路由，失败则尝试下一条
	for _, route := range routes {
		client := llm.NewDMXAPIClientWithConfig(route.BaseURL, route.APIKey)
		routerCtx := llm.WithLLMRouterMark(ctx)

		var content string
		var usage *billing.TokenUsage
		var callErr error

		log.Infow("LLMRouter: dispatching request",
			"provider", route.ProviderName,
			"model", route.ProviderModelID,
			"thinking_format", route.ThinkingFormat,
		)

		if route.ThinkingFormat == ThinkingGemini {
			// Gemini：走原生端点 /v1beta/models/{model}:streamGenerateContent
			content, usage, callErr = client.StreamGeminiGenerate(
				routerCtx,
				route.ProviderModelID,
				messages,
				onEvent,
			)
		} else {
			// 其他模型：走 OpenAI 兼容端点 /v1/chat/completions
			content, usage, callErr = client.StreamChatCompletion(
				routerCtx,
				route.ProviderModelID,
				messages,
				temperature,
				maxTokens,
				route.ThinkingFormat,
				onEvent,
			)
		}

		if callErr != nil {
			log.Warnw("LLMRouter: route failed, trying next",
				"provider", route.ProviderName,
				"model", route.ProviderModelID,
				"error", callErr,
			)
			lastErr = callErr
			continue
		}

		// 3. 成功：记录 Langfuse generation
		if tc := langfuse.FromContext(ctx); tc != nil {
			genID := langfuse.SpanID()
			genOpts := []langfuse.GenOption{
				langfuse.WithGenName("llm-chat"),
				langfuse.WithGenModel(route.ProviderModelID),
				langfuse.WithGenParent(tc.ParentObservationID),
				langfuse.WithGenInput(messages),
				langfuse.WithGenOutput(content),
			}
			langfuse.CreateGeneration(tc.TraceID, genID, genOpts...)
			var endOpts []langfuse.GenOption
			if usage != nil {
				endOpts = append(endOpts, langfuse.WithGenUsage(usage.PromptTokens, usage.CompletionTokens))
			}
			langfuse.EndGeneration(genID, endOpts...)
		}

		// 4. 成功：记录 billing 用量
		if bc := billing.FromContext(ctx); bc != nil && usage != nil {
			billing.RecordLLM(bc.UserID, route.ProviderName, route.ProviderModelID, bc.Operation, usage, bc.Meta)
		}

		// 5. 回写实际使用的模型名到 usage，供上层持久化
		if usage != nil {
			usage.ModelName = route.ProviderModelID
		}

		return content, usage, nil
	}

	// 所有路由失败
	return "", nil, fmt.Errorf("llmrouter.StreamChat: all routes failed for model %q: %w", modelKey, lastErr)
}

// InvalidateCache 清空路由缓存，使下次调用重新从 DB 加载
func (r *Router) InvalidateCache() {
	r.cache.Invalidate()
}

// inferThinkingFormat 根据 DMXAPI 供应商侧模型 ID 推断 thinking 激活方式
//
// DMXAPI 文档参考：
//   - Claude: -thinking 模型名后缀，走 /v1/chat/completions，reasoning_content 字段
//   - Gemini: 原生 /v1beta/models/{model}:streamGenerateContent 端点，part.thought 属性
//   - Qwen:   enable_thinking: true，走 /v1/chat/completions
//   - Doubao: thinking: {type:"enabled"}，走 /v1/chat/completions
//   - GPT:    thinking-only 模型，不接受任何 thinking 参数（会 400）
//   - DeepSeek: OpenAI 通用格式，DMXAPI 无单独 thinking 文档
func inferThinkingFormat(providerModelID string) string {
	id := strings.ToLower(providerModelID)

	// Claude：通过 DMXAPI 模型名后缀 -thinking 激活（provider_model_id 已含后缀）
	// 不需要额外参数，但 temperature 必须为 1（在 StreamChatCompletion 中处理）
	if strings.Contains(id, "claude") {
		return ThinkingNone
	}

	// GPT / OpenAI o-系列：thinking-only 模型，发任何 thinking 参数会 400
	if strings.Contains(id, "gpt") || strings.HasPrefix(id, "o1") || strings.HasPrefix(id, "o3") || strings.HasPrefix(id, "o4") {
		return ThinkingNone
	}

	// DeepSeek：OpenAI 通用格式，DMXAPI 无 thinking 参数支持
	if strings.Contains(id, "deepseek") {
		return ThinkingNone
	}

	// Doubao / 豆包：thinking:{type:"enabled"}，走 OpenAI 兼容端点
	if strings.Contains(id, "doubao") {
		return ThinkingDoubao
	}

	// Gemini：原生端点，thinking 通过 part.thought 属性返回
	if strings.Contains(id, "gemini") {
		return ThinkingGemini
	}

	// Qwen 等：enable_thinking: true
	return ThinkingEnableField
}
