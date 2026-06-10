// Package chatbot — ChatStream 流式对话核心逻辑
package chatbot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/numind/biz/contextbudget"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/billing"
	cb "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/retrieve"

	"gorm.io/gorm"
)

// ChatbotChatOperation is the billing operation string used for chatbot chat calls.
// Exported so tests can assert the operation without duplicating the constant.
const ChatbotChatOperation = "chatbot_chat"

// chatStreamRecentTurns is the number of most-recent history turns treated as
// RoleRecent (the rest are classified as RoleDurable for compression purposes).
const chatStreamRecentTurns = 10

// chatStreamMaxHistory 历史消息上下文窗口（最近 N 条消息）
const chatStreamMaxHistory = 20

// chatStreamMaxChunks 检索 rerank 后保留的切片数（喂给 LLM 的知识条数上限）
const chatStreamMaxChunks = 6

// chatStreamRetrieveTopK 每路召回 limit（底座 parallelSearch 用），rerank 前的候选池。
const chatStreamRetrieveTopK = 10

// chatStreamRetrieveBillingLabel 底座检索的计费/trace 归因标签。
const chatStreamRetrieveBillingLabel = "chatbot_retrieval"

// chatbotGroundingPrompt 是挂载知识库时注入的硬约束 grounding system fragment 文案。
// 仅在检索到 KB chunks 时加入（纯聊天 chatbot 不加），把回答严格锚定到检索资料，
// 修复"回答怪怪的"（裸检索无 grounding 时 LLM 会脱离资料自由发挥/编造）。
const chatbotGroundingPrompt = "以下消息中标注为「知识库资料」的内容是从知识库检索到的资料。" +
	"请仅依据这些资料回答用户的问题；资料中未提及的内容不要编造或臆测。" +
	"若检索到的资料不足以回答，请如实说明「知识库中暂无相关信息」，不要凭空作答。" +
	"引用资料时可用 [知识N] 标注来源编号（N 对应资料前缀），便于用户核对出处。"

// scoreToImportance maps a rerank score in [0.0, 1.0] to an integer importance
// in [0, 10] for use in ContextFragment.Importance. A score of 1.0 maps to 10;
// 0.0 maps to 0. Values outside [0.0, 1.0] are clamped.
//
// NOTE: this is a verbatim copy of salesrag.scoreToImportance (an unexported
// 12-line pure helper). Copied rather than cross-package imported because the
// salesrag original is package-private; duplicating a trivial pure function is
// cheaper than widening the salesrag API surface for a single caller.
func scoreToImportance(score float32) int {
	if score <= 0 {
		return 0
	}
	if score >= 1.0 {
		return 10
	}
	return int(score * 10)
}

// BuildChatContextFragments constructs the ordered ContextFragment slice for a
// chatbot chat turn. This is the canonical fragment factory for the chatbot
// producer (spec §9.2 Task 10 mapping).
//
// Fragment layout:
//
//	[0]   system prompt → RoleImmutable, CompressNone, Critical=true
//	[1…N] history turns (oldest first):
//	        older half → RoleDurable, CompressSummarize
//	        recent half → RoleRecent, CompressSummarize
//	[g]   grounding instruction (ONLY when kbChunks present) → RoleImmutable system
//	[N+1] KB evidence chunks → RoleEvidence, SourceKB, CompressReference
//	        Content prefixed with "[知识N] (相关度:X%) " for citation/grounding
//	        Importance derived from chunk.Score (scoreToImportance)
//	[last] current user message → RoleRecent, SourceUser, Critical=true, CompressNone
//
// Grounding contract (numind 知识库 RAG 修复 — "回答怪怪的" 根因):
// when kbChunks is non-empty a hard-constraint grounding system fragment is
// inserted (after history, before evidence) so the LLM answers ONLY from the
// retrieved material. When kbChunks is empty the bot stays a 纯聊天 assistant —
// NO grounding fragment, NO evidence, system prompt untouched.
//
// history is the persisted message slice ordered oldest-first (role+content pairs
// as model.ChatbotMessage). kbChunks is the list of retrieved knowledge chunks.
// The recentThreshold parameter controls how many of the last history turns are
// classified as RoleRecent vs RoleDurable; pass chatStreamRecentTurns for the
// production default.
func BuildChatContextFragments(
	systemPrompt string,
	history []model.ChatbotMessage,
	currentMessage string,
	kbChunks []domain.KnowledgeChunk,
	recentThreshold int,
) []cb.ContextFragment {
	var frags []cb.ContextFragment
	order := 0

	// Fragment 0: system prompt (immutable, never compressed).
	frags = append(frags, contextbudget.NewImmutableSystemFragment("sys-0", systemPrompt, order))
	order++

	// Fragments 1…N: history turns.
	// The most recent `recentThreshold` turns are RoleRecent; older turns are RoleDurable.
	histLen := len(history)
	durableEnd := histLen - recentThreshold
	if durableEnd < 0 {
		durableEnd = 0
	}

	for i, msg := range history {
		id := fmt.Sprintf("hist-%d", i)
		if i < durableEnd {
			// Older turn → durable (can be summarised).
			if msg.Role == "assistant" {
				frags = append(frags, contextbudget.NewDurableAssistantFragment(id, msg.Content, order, 4))
			} else {
				frags = append(frags, contextbudget.NewDurableUserFragment(id, msg.Content, order, 4))
			}
		} else {
			// Recent turn → recent (preserve under moderate pressure).
			src := cb.SourceUser
			if msg.Role == "assistant" {
				src = cb.SourceAssistant
			}
			frags = append(frags, cb.ContextFragment{
				ID:              id,
				Role:            cb.RoleRecent,
				Source:          src,
				ContentType:     cb.ContentText,
				Content:         msg.Content,
				Importance:      6,
				Order:           order,
				Compressibility: cb.CompressSummarize,
			})
		}
		order++
	}

	// Grounding instruction + KB evidence chunks — ONLY when KB chunks exist.
	// Placed AFTER history, BEFORE evidence: this keeps the system-prompt head and
	// the history prefix byte-stable across retrievals (prompt-cache invariant,
	// prefix_stability_test) while still ranking the grounding constraint above the
	// retrieved material. Pure-chat bots (no kbChunks) skip this whole block and
	// thus emit neither grounding nor evidence — identical to legacy 纯聊天 behavior.
	if len(kbChunks) > 0 {
		// Grounding system fragment: hard constraint to answer only from retrieved KB.
		frags = append(frags, contextbudget.NewImmutableSystemFragment("kb-grounding", chatbotGroundingPrompt, order))
		order++

		for i, chunk := range kbChunks {
			chunkRef := chunk.ID
			if chunkRef == "" {
				chunkRef = fmt.Sprintf("kb-chunk-%d", i)
			}
			// Prefix evidence with a citable header so the LLM can reference sources
			// as [知识N]; relevance is the rerank score as a percentage.
			relevancePct := int(chunk.Score * 100)
			labeledContent := fmt.Sprintf("【知识库资料】[知识%d] (相关度:%d%%)\n%s", i+1, relevancePct, chunk.Content)
			frags = append(frags, contextbudget.NewEvidenceReferenceFragment(
				fmt.Sprintf("kb-%d", i),
				chunkRef,
				labeledContent,
				order,
				scoreToImportance(chunk.Score),
			))
			order++
		}
	}

	// Current user message: critical, RoleRecent, CompressNone. order is the
	// running max here, so it renders last after the budget middleware's
	// Order-stable sort. Use the shared producer so the Order contract is enforced
	// in one place. (sop-context-ordering-fix)
	frags = append(frags, contextbudget.NewCriticalUserFragment("cur-msg", currentMessage, order))

	return frags
}

// ChatStream 执行流式对话：向量检索 → 组装 prompt → LLM 流式输出 → 持久化消息
func (b *chatbotBiz) ChatStream(ctx context.Context, userID uint, sessionID uint, message string, modelKey string, thinking bool, handler StreamHandler) error {
	// 1. 获取会话并验证所有权
	session, err := b.ds.ChatbotSession().GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrSessionNotFound
		}
		return fmt.Errorf("ChatStream: get session: %w", err)
	}
	if session.UserID != userID {
		return errno.ErrForbidden
	}

	// 1a. 运行时权限守卫 —— 即便持有 session，当前对 chatbot 的白名单也必须仍然命中。
	// 实现 PRD AS-5「撤销即时生效」：父账号撤权后，子账号对已有 session 再发消息直接 403。
	// 放在 session 所有权校验之后、获取 chatbot config / LLM 调用之前（child-run-permission §3.5，
	// S3 Gate review P1-B 修正：原 plan 引用的 `Chat` 方法不存在，真正入口是 ChatStream）。
	//
	// 性能说明：HasChatbotPermission 内部每条 message 多 2 次 DB 查询
	// （user.First 判 parent / 子账号 + 白名单 Count），父账号走 bypass 省一次 Count。
	// 当前无 Redis 缓存；若 chatbot QPS 升高可考虑在 store 层加短 TTL（10-60s）缓存，
	// 与「撤销即时生效」的要求做 trade-off（缓存 TTL 即撤权最大延迟）。
	ok, err := b.ds.Customers().HasChatbotPermission(ctx, userID, session.ChatbotID)
	if err != nil {
		return fmt.Errorf("ChatStream permission: %w", err)
	}
	if !ok {
		return errno.ErrChatbotRunDenied
	}

	// 2. 获取智能体配置并验证可访问性
	config, err := b.ds.ChatbotConfig().Get(ctx, session.ChatbotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrChatbotNotFound
		}
		return fmt.Errorf("ChatStream: get chatbot config: %w", err)
	}
	if config.Status != model.ChatbotStatusPublished && config.UserID != userID {
		return errno.ErrChatbotNotPublished
	}

	// 3. Langfuse: 创建 trace
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, "chatbot-chat",
		langfuse.WithUserID(userID),
		langfuse.WithSessionID(fmt.Sprintf("chatbot-session-%d", sessionID)),
		langfuse.WithTraceInput(map[string]interface{}{
			"chatbot_id":   session.ChatbotID,
			"chatbot_name": config.Name,
			"session_id":   sessionID,
			"user_id":      userID,
			"message":      message,
		}),
		langfuse.WithTraceTags("chatbot", "chat"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)

	// 4. 创建 context-assembly span
	assemblySpanID := langfuse.SpanID()
	langfuse.CreateSpan(traceID, assemblySpanID, "context-assembly",
		langfuse.WithSpanInput(map[string]interface{}{"message": message}),
	)

	// 4a. 先取历史窗口（供改写多轮消歧 + fragment 构建复用，避免二次查询）。
	historyMsgs := b.fetchRecentHistory(ctx, session.ID)

	// 5. 知识库检索（走底座 retrieve.Service：query 改写 + 多路检索 + rerank + 严格 scope）。
	//
	// 产品决策：只有挂了知识库且解析出 docIDs 时才走底座检索 + grounding；
	// 没挂知识库（或解析不出 docIDs）= 纯聊天，不检索、不 grounding、不报错（保持现状）。
	// 绝不对纯聊天 chatbot 调底座（否则触发 retrieve.ErrEmptyScope）。
	var retrievedChunks []domain.KnowledgeChunk
	var retrievedChunkContents []string // for buildChatMessages (legacy path)
	vectorSpanID := langfuse.SpanID()
	langfuse.CreateSpan(traceID, vectorSpanID, "vector-retrieval",
		langfuse.WithSpanParent(assemblySpanID),
	)

	kbs, err := b.ds.ChatbotConfig().ListMountedKBs(ctx, session.ChatbotID)
	if err != nil {
		log.C(ctx).Warnw("ChatStream: list mounted KBs failed", "error", err)
	}

	if len(kbs) > 0 {
		var kbIDs []uint
		for _, kb := range kbs {
			kbIDs = append(kbIDs, kb.ID)
		}

		docIDs, docErr := b.ds.KnowledgeBase().ListDocumentIDsByKBs(ctx, kbIDs)
		if docErr != nil {
			log.C(ctx).Warnw("ChatStream: list document IDs failed", "error", docErr)
		}

		if len(docIDs) > 0 && b.retrieveSvc != nil {
			// 注入 owner userID 供底座 rerank 计费/隔离归因（与 scope.UserID 一致）。
			retrieveCtx := middleware.NewContextWithUserID(ctx, config.UserID)
			result, retErr := b.retrieveSvc.Retrieve(retrieveCtx, message,
				retrieve.Scope{
					UserID:      config.UserID, // 使用智能体所有者的 userID 做隔离
					DocumentIDs: docIDs,
				},
				retrieve.Options{
					TopK:         chatStreamRetrieveTopK,
					RerankTopN:   chatStreamMaxChunks,
					RewriteQuery: true,
					History:      historyToStrings(historyMsgs),
					BillingLabel: chatStreamRetrieveBillingLabel,
				},
			)
			if retErr != nil {
				// 检索失败降级为无资料回答（不阻断对话），与原裸检索失败语义一致。
				log.C(ctx).Warnw("ChatStream: base retrieval failed", "error", retErr)
				langfuse.EndSpan(traceID, vectorSpanID, langfuse.WithSpanError(retErr.Error()))
			} else {
				retrievedChunks = result.Chunks
				for _, chunk := range retrievedChunks {
					retrievedChunkContents = append(retrievedChunkContents, chunk.Content)
				}
				langfuse.EndSpan(traceID, vectorSpanID, langfuse.WithSpanOutput(map[string]interface{}{
					"chunk_count": len(retrievedChunks),
				}))
			}
		} else {
			langfuse.EndSpan(traceID, vectorSpanID, langfuse.WithSpanOutput(map[string]interface{}{
				"chunk_count": 0,
				"reason":      "no docs or retrieval service unavailable",
			}))
		}
	} else {
		langfuse.EndSpan(traceID, vectorSpanID, langfuse.WithSpanOutput(map[string]interface{}{
			"chunk_count": 0,
			"reason":      "no KBs mounted (纯聊天)",
		}))
	}

	// 6. 构建 prompt (legacy path) + ContextFragments (budget path)
	promptSpanID := langfuse.SpanID()
	langfuse.CreateSpan(traceID, promptSpanID, "prompt-construction",
		langfuse.WithSpanParent(assemblySpanID),
	)

	messages := b.buildChatMessages(config, historyMsgs, message, retrievedChunkContents)

	// Build context fragments for the context-budget middleware.
	// System prompt: config.SystemPrompt (KB context is embedded via KB evidence fragments,
	// not prepended to system prompt here — the fragment renderer handles placement).
	ctxFragments := BuildChatContextFragments(
		config.SystemPrompt,
		historyMsgs,
		message,
		retrievedChunks,
		chatStreamRecentTurns,
	)

	langfuse.EndSpan(traceID, promptSpanID, langfuse.WithSpanOutput(map[string]interface{}{
		"message_count":   len(messages),
		"fragment_count":  len(ctxFragments),
		"has_context":     len(retrievedChunks) > 0,
		"history_fetched": true,
	}))

	// 结束 context-assembly span
	langfuse.EndSpan(traceID, assemblySpanID)

	// 7. 注入计费上下文 + skip-legacy-billing 标记，通过 AI Gateway 调用流式 LLM
	ctx = billing.WithBilling(ctx, userID, ChatbotChatOperation)
	// 将 userID 注入 aiservice middleware context，使 Tracing/Billing 中间件能正确读取（避免 user_id=0）
	ctx = aismw.WithUserID(ctx, userID)
	// ParentObservationID 设为空字符串，使 Gateway 创建的 generation 直接挂在 trace 根节点下
	ctx = langfuse.WithTraceAndParent(ctx, traceID, "")
	// 注入 skip-legacy-billing：Gateway 已统一记账，防止旧 billing 路径双记
	ctx = aiservice.WithSkipLegacyBilling(ctx)
	ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("chatbot_session:%d", sessionID))

	// 将 messages ([]map[string]interface{}) 转换为 []aiservice.ChatMessage
	aiMessages := make([]aiservice.ChatMessage, 0, len(messages))
	for _, m := range messages {
		role, _ := m["role"].(string)
		content, _ := m["content"].(string)
		aiMessages = append(aiMessages, aiservice.ChatMessage{
			Role:    aiservice.MessageRole(role),
			Content: aiservice.MessageContent{Text: content},
		})
	}

	var fullContent strings.Builder
	var thinkingContent strings.Builder

	// 调用 Gateway ChatStream（profile.ChatbotStream 任务配置由 Registry 解析模型）
	// ContextFragments 供 context-budget middleware 使用；Messages 保持向后兼容（nil fragments fallback）。
	gatewayReq := aiservice.ChatRequest{
		Messages:         aiMessages,
		ContextFragments: ctxFragments,
		Temperature:      0.7,
		ModelOverride:    modelKey, // pass user's model choice; empty = use task profile default
		Thinking:         thinking,
	}

	ch, llmErr := aiservice.ChatStream(ctx, profile.ChatbotStream, gatewayReq)
	if llmErr != nil {
		return fmt.Errorf("ChatStream: LLM call failed: %w", llmErr)
	}

	var gatewayUsage *billing.TokenUsage
	var modelName string
	var streamErr error
	for chunk := range ch {
		if chunk.Model != "" {
			modelName = chunk.Model
		}
		if chunk.ReasoningDelta != "" {
			thinkingContent.WriteString(chunk.ReasoningDelta)
			if handlerErr := handler("thinking", map[string]string{"content": chunk.ReasoningDelta}); handlerErr != nil {
				log.C(ctx).Warnw("ChatStream: handler error on thinking chunk", "error", handlerErr)
			}
		}
		if chunk.Delta != "" {
			fullContent.WriteString(chunk.Delta)
			if handlerErr := handler("token", map[string]string{"content": chunk.Delta}); handlerErr != nil {
				log.C(ctx).Warnw("ChatStream: handler error on token chunk", "error", handlerErr)
			}
		}
		if chunk.IsFinal {
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
			if chunk.Usage != nil {
				gatewayUsage = &billing.TokenUsage{
					PromptTokens:     chunk.Usage.PromptTokens,
					CompletionTokens: chunk.Usage.CompletionTokens,
					TotalTokens:      chunk.Usage.TotalTokens,
					ReasoningTokens:  chunk.Usage.ReasoningTokens,
					ModelName:        modelName,
				}
			}
		}
	}
	if streamErr != nil {
		// Log and return so the SSE handler can emit an error event to the
		// client instead of silently treating a truncated reply as success.
		log.C(ctx).Warnw("ChatStream: stream ended with error",
			"session_id", sessionID, "error", streamErr)
		return fmt.Errorf("ChatStream: stream error: %w", streamErr)
	}
	if gatewayUsage == nil {
		log.C(ctx).Warnw("ChatStream: stream ended without final usage chunk", "session_id", sessionID)
	}
	usage := gatewayUsage

	// 9. 持久化消息（用户消息 + 助手消息）
	maxSeq, seqErr := b.ds.ChatbotSession().GetMaxSeq(ctx, sessionID)
	if seqErr != nil {
		log.C(ctx).Warnw("ChatStream: get max seq failed", "error", seqErr)
	}

	userMsg := &model.ChatbotMessage{
		SessionID: sessionID,
		UserID:    userID,
		Role:      "user",
		Content:   message,
		TraceID:   traceID,
		Seq:       maxSeq + 1,
		CreatedAt: time.Now(),
	}
	if err := b.ds.ChatbotSession().CreateMessage(ctx, userMsg); err != nil {
		log.C(ctx).Errorw("ChatStream: save user message failed", "error", err)
	}

	var promptTokens, completionTokens int
	var assistantModelName string
	if usage != nil {
		promptTokens = usage.PromptTokens
		completionTokens = usage.CompletionTokens
		assistantModelName = usage.ModelName
	}

	assistantMsg := &model.ChatbotMessage{
		SessionID:        sessionID,
		UserID:           userID,
		Role:             "assistant",
		Content:          fullContent.String(),
		Thinking:         thinkingContent.String(),
		TraceID:          traceID,
		Seq:              maxSeq + 2,
		ModelName:        assistantModelName,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CreatedAt:        time.Now(),
	}
	if err := b.ds.ChatbotSession().CreateMessage(ctx, assistantMsg); err != nil {
		log.C(ctx).Errorw("ChatStream: save assistant message failed", "error", err)
	}

	// 递增会话消息计数（+2: user + assistant）
	_ = b.ds.ChatbotSession().IncrementMessageCount(ctx, sessionID)
	_ = b.ds.ChatbotSession().IncrementMessageCount(ctx, sessionID)

	// 10. 更新 trace output
	langfuse.CreateTrace(traceID, "chatbot-chat",
		langfuse.WithTraceOutput(map[string]interface{}{
			"response_length":   fullContent.Len(),
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
		}),
	)

	// 11. 发送完成事件（含被引用的知识库来源，供前端显示出处）。
	doneData := map[string]interface{}{
		"trace_id": traceID,
	}
	if usage != nil {
		doneData["prompt_tokens"] = usage.PromptTokens
		doneData["completion_tokens"] = usage.CompletionTokens
	}
	if sources := parseCitedSources(fullContent.String(), retrievedChunks); len(sources) > 0 {
		doneData["sources"] = sources
	}
	return handler("done", doneData)
}

// citationRe 匹配回答中形如 [1] / [知识2] 的引用编号（编号 1-based）。
var citationRe = regexp.MustCompile(`\[(?:知识)?(\d+)\]`)

// CitedSource 是 done 事件里回填的被引用知识库来源。index 为 1-based 引用编号，
// 对应 BuildChatContextFragments evidence 前缀的 [知识N]。
type CitedSource struct {
	Index        int    `json:"index"`
	ChunkID      string `json:"chunk_id"`
	DocumentID   uint   `json:"document_id"`
	DocumentName string `json:"document_name,omitempty"`
}

// parseCitedSources 解析回答 fullContent 中的 [N]/[知识N] 引用，映射回检索到的 chunks，
// 返回去重、按引用编号升序的来源列表。越界编号忽略；无引用或无 chunks 返回 nil。
//
// 最小后端实现：done 事件加 sources 字段即可，不改既有 SSE 协议结构（不强制前端消费）。
func parseCitedSources(fullContent string, chunks []domain.KnowledgeChunk) []CitedSource {
	if fullContent == "" || len(chunks) == 0 {
		return nil
	}
	matches := citationRe.FindAllStringSubmatch(fullContent, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[int]bool)
	var sources []CitedSource
	for _, m := range matches {
		n, convErr := strconv.Atoi(m[1])
		if convErr != nil || n < 1 || n > len(chunks) || seen[n] {
			continue
		}
		seen[n] = true
		chunk := chunks[n-1] // [知识N] 是 1-based，对应 chunks[N-1]
		sources = append(sources, CitedSource{
			Index:        n,
			ChunkID:      chunk.ID,
			DocumentID:   chunk.DocumentID,
			DocumentName: chunk.DocumentName,
		})
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].Index < sources[j].Index })
	return sources
}

// buildChatMessages 组装 LLM 消息数组：system + history + user。
//
// 注意：此为 legacy []map 路径，当 ContextFragments 非空时其输出会被 context-budget
// 中间件用 RenderContextFragments 的结果覆盖（见 aiservice/middleware/context_budget.go）。
// 因此 grounding/引用前缀只加在 BuildChatContextFragments，不动这里。history 由调用方
// 预取并传入（与 fragment 构建复用同一批历史，避免二次查询）。
func (b *chatbotBiz) buildChatMessages(config *model.ChatbotConfig, historyMsgs []model.ChatbotMessage, userMessage string, retrievedChunks []string) []map[string]interface{} {
	var messages []map[string]interface{}

	// 1. system prompt
	systemPrompt := config.SystemPrompt
	if len(retrievedChunks) > 0 {
		systemPrompt += "\n\n参考资料：\n" + strings.Join(retrievedChunks, "\n\n")
	}
	messages = append(messages, map[string]interface{}{
		"role":    "system",
		"content": systemPrompt,
	})

	// 2. 历史消息（最近 N 条，按 seq 升序）
	for _, msg := range historyMsgs {
		messages = append(messages, map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	// 3. 当前用户消息
	messages = append(messages, map[string]interface{}{
		"role":    "user",
		"content": userMessage,
	})

	return messages
}

// fetchRecentHistory 取会话最近 chatStreamMaxHistory 条消息（按 seq 升序）。
// 用 offset 技巧：先取总数，超过窗口则只取最后 N 条。失败仅告警并返回 nil（不阻断对话）。
// 供 ChatStream 检索改写 + fragment 构建 + legacy buildChatMessages 复用同一批历史。
func (b *chatbotBiz) fetchRecentHistory(ctx context.Context, sessionID uint) []model.ChatbotMessage {
	historyMsgs, total, err := b.ds.ChatbotSession().ListMessages(ctx, sessionID, 0, chatStreamMaxHistory)
	if err != nil {
		log.C(ctx).Warnw("ChatStream: fetch history failed", "error", err)
		return nil
	}
	if total > int64(chatStreamMaxHistory) {
		offset := int(total) - chatStreamMaxHistory
		historyMsgs, _, err = b.ds.ChatbotSession().ListMessages(ctx, sessionID, offset, chatStreamMaxHistory)
		if err != nil {
			log.C(ctx).Warnw("ChatStream: fetch recent history failed", "error", err)
			return nil
		}
	}
	return historyMsgs
}

// historyToStrings 把历史消息扁平化为 "role: content" 行，供底座 QueryRewriter 做多轮
// 指代消歧。空历史返回 nil。
func historyToStrings(historyMsgs []model.ChatbotMessage) []string {
	if len(historyMsgs) == 0 {
		return nil
	}
	out := make([]string, 0, len(historyMsgs))
	for _, msg := range historyMsgs {
		out = append(out, msg.Role+": "+msg.Content)
	}
	return out
}
