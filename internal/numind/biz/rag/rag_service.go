package rag

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/config"
	"numind-server/internal/pkg/log"

	"github.com/philippgille/chromem-go"
	"github.com/spf13/viper"
)

// contains 检查字符串是否包含子字符串（不区分大小写）
func contains(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

// RagService RAG服务结构体
type RagService struct {
	db           *chromem.DB
	collection   *chromem.Collection
	aliBiz       ali.AliBiz
	configReader *config.ConfigReader
}

// NewRagService 创建新的RAG服务实例
func NewRagService(aliBiz ali.AliBiz, configReader *config.ConfigReader, dbPath string) (*RagService, error) {
	// 初始化持久化向量数据库
	db, err := chromem.NewPersistentDB(dbPath, false)
	if err != nil {
		return nil, fmt.Errorf("初始化向量数据库失败: %w", err)
	}

	// 获取或创建默认的 collection（用于存储所有笔记）
	// 使用一个空的 embeddingFunc，因为我们手动提供 embedding
	collection, err := db.GetOrCreateCollection("books", map[string]string{}, nil)
	if err != nil {
		return nil, fmt.Errorf("获取或创建collection失败: %w", err)
	}

	return &RagService{
		db:           db,
		collection:   collection,
		aliBiz:       aliBiz,
		configReader: configReader,
	}, nil
}

// ChatWithRAG 基于笔记进行RAG对话
// userID: 用户ID，用于数据隔离
// question: 用户问题
// bookIDs: 必填的笔记ID数组，用于基于多个笔记进行聊天
func (r *RagService) ChatWithRAG(ctx context.Context, userID uint, question string, bookIDs []uint) (string, error) {
	if question == "" {
		return "", fmt.Errorf("问题不能为空")
	}

	if len(bookIDs) == 0 {
		return "", fmt.Errorf("book_ids 不能为空")
	}

	// 1. 将问题转化为向量
	log.C(ctx).Infow("开始生成问题向量", "user_id", userID, "question", question, "book_ids", bookIDs)
	questionEmbedding, err := r.aliBiz.QianwenEmbedding(question)
	if err != nil {
		log.C(ctx).Errorw("生成问题向量失败", "error", err, "user_id", userID)
		return "", fmt.Errorf("生成问题向量失败: %w", err)
	}

	// 2. 将 bookIDs 转换为字符串集合，用于快速查找
	bookIDSet := make(map[string]bool)
	for _, bookID := range bookIDs {
		bookIDSet[strconv.FormatUint(uint64(bookID), 10)] = true
	}

	// 3. 调用 chromem 检索相似笔记（检索所有用户笔记，然后在内存中过滤）
	userIDStr := strconv.FormatUint(uint64(userID), 10)
	where := map[string]string{
		"user_id": userIDStr,
	}

	// 检索相似笔记，使用动态调整策略避免超过集合文档数
	// 先尝试较大的数量，如果失败则逐步减少
	var results []chromem.Result
	nResultsOptions := []int{50, 30, 20, 10, 5, 3, 1}
	var queryErr error

	for _, nResults := range nResultsOptions {
		results, queryErr = r.collection.QueryEmbedding(ctx, questionEmbedding, nResults, where, nil)
		if queryErr == nil {
			// 成功获取结果，跳出循环
			log.C(ctx).Infow("检索相似笔记成功", "user_id", userID, "n_results", nResults, "actual_count", len(results))
			break
		}
		// 如果是文档数量不足的错误，尝试下一个更小的数量
		if contains(queryErr.Error(), "nResults must be") {
			log.C(ctx).Warnw("请求结果数超过集合文档数，尝试减少", "user_id", userID, "n_results", nResults, "error", queryErr)
			continue
		}
		// 其他错误直接返回
		log.C(ctx).Errorw("检索相似笔记失败", "error", queryErr, "user_id", userID)
		return "", fmt.Errorf("检索相似笔记失败: %w", queryErr)
	}

	// 如果所有尝试都失败，返回错误
	if queryErr != nil {
		log.C(ctx).Errorw("检索相似笔记失败，所有尝试都失败", "error", queryErr, "user_id", userID)
		return "", fmt.Errorf("检索相似笔记失败: %w", queryErr)
	}

	log.C(ctx).Infow("检索到相似笔记", "user_id", userID, "count", len(results), "target_book_ids", bookIDs)

	// 4. 内存过滤：筛选出属于当前用户且属于指定 bookIDs 的笔记，按相似度排序取 Top 3
	var filteredBooks []chromem.Result
	for _, result := range results {
		// 验证用户ID
		if userIDFromMeta, ok := result.Metadata["user_id"]; !ok || userIDFromMeta != userIDStr {
			continue
		}
		// 验证笔记ID是否在指定的 bookIDs 中
		if bookIDFromMeta, ok := result.Metadata["book_id"]; ok && bookIDSet[bookIDFromMeta] {
			filteredBooks = append(filteredBooks, result)
			if len(filteredBooks) >= 3 {
				break
			}
		}
	}

	log.C(ctx).Infow("过滤后的笔记", "user_id", userID, "count", len(filteredBooks), "book_ids", bookIDs)

	// 4. Prompt 组装：将筛选出的笔记内容拼接成 System Prompt
	var contextText string
	if len(filteredBooks) == 0 {
		contextText = "未找到相关笔记内容。"
	} else {
		contextText = "基于以下笔记回答用户问题：\n\n"
		for i, book := range filteredBooks {
			content := book.Metadata["content"]
			if content == "" {
				content = book.Content
			}
			contextText += fmt.Sprintf("【笔记 %d】\n%s\n\n", i+1, content)
		}
	}

	// 5. 调用 LLM：构造 messages，调用现有的 aliBiz.QianwenTextStream 获取回答
	systemPrompt := fmt.Sprintf(`你是一位智能助手，专门帮助用户基于他们创建的笔记内容回答问题。

%s

## 用户问题
%s

## 回答要求
1. 基于上述笔记内容回答用户的问题
2. 如果笔记中包含相关信息，请优先使用这些信息
3. 如果笔记中没有相关信息，可以基于你的知识回答，但要说明这是基于通用知识
4. 回答要准确、简洁、有帮助
5. 使用中文回答

请直接回答用户的问题，不要包含"根据上下文"等前缀。`, contextText, question)

	messages := []map[string]string{
		{"role": "user", "content": systemPrompt},
	}

	log.C(ctx).Infow("开始调用LLM生成回答", "user_id", userID)
	answer, err := r.aliBiz.QianwenTextStream(messages, 2000, 0.7)
	if err != nil {
		log.C(ctx).Errorw("LLM生成回答失败", "error", err, "user_id", userID)
		return "", fmt.Errorf("LLM生成回答失败: %w", err)
	}

	log.C(ctx).Infow("RAG对话完成", "user_id", userID, "answer_length", len(answer))
	return answer, nil
}

// ChatWithRAGStream 基于笔记进行RAG对话（流式版本）
// userID: 用户ID，用于数据隔离
// question: 用户问题
// bookIDs: 必填的笔记ID数组，用于基于多个笔记进行聊天
// deepThinking: 是否开启深度思考模式
// handler: 流式处理函数，每收到一个chunk就调用一次
func (r *RagService) ChatWithRAGStream(ctx context.Context, userID uint, question string, bookIDs []uint, deepThinking bool, handler func(chunk string) error) error {
	if question == "" {
		return fmt.Errorf("问题不能为空")
	}

	if len(bookIDs) == 0 {
		return fmt.Errorf("book_ids 不能为空")
	}

	// 1. 将问题转化为向量
	log.C(ctx).Infow("开始生成问题向量", "user_id", userID, "question", question, "book_ids", bookIDs)
	questionEmbedding, err := r.aliBiz.QianwenEmbedding(question)
	if err != nil {
		log.C(ctx).Errorw("生成问题向量失败", "error", err, "user_id", userID)
		return fmt.Errorf("生成问题向量失败: %w", err)
	}

	// 2. 将 bookIDs 转换为字符串集合，用于快速查找
	bookIDSet := make(map[string]bool)
	for _, bookID := range bookIDs {
		bookIDSet[strconv.FormatUint(uint64(bookID), 10)] = true
	}

	// 3. 调用 chromem 检索相似笔记（检索所有用户笔记，然后在内存中过滤）
	userIDStr := strconv.FormatUint(uint64(userID), 10)
	where := map[string]string{
		"user_id": userIDStr,
	}

	// 检索相似笔记，使用动态调整策略避免超过集合文档数
	// 先尝试较大的数量，如果失败则逐步减少
	var results []chromem.Result
	nResultsOptions := []int{50, 30, 20, 10, 5, 3, 1}
	var queryErr error

	for _, nResults := range nResultsOptions {
		results, queryErr = r.collection.QueryEmbedding(ctx, questionEmbedding, nResults, where, nil)
		if queryErr == nil {
			// 成功获取结果，跳出循环
			log.C(ctx).Infow("检索相似笔记成功", "user_id", userID, "n_results", nResults, "actual_count", len(results))
			break
		}
		// 如果是文档数量不足的错误，尝试下一个更小的数量
		if contains(queryErr.Error(), "nResults must be") {
			log.C(ctx).Warnw("请求结果数超过集合文档数，尝试减少", "user_id", userID, "n_results", nResults, "error", queryErr)
			continue
		}
		// 其他错误直接返回
		log.C(ctx).Errorw("检索相似笔记失败", "error", queryErr, "user_id", userID)
		return fmt.Errorf("检索相似笔记失败: %w", queryErr)
	}

	// 如果所有尝试都失败，返回错误
	if queryErr != nil {
		log.C(ctx).Errorw("检索相似笔记失败，所有尝试都失败", "error", queryErr, "user_id", userID)
		return fmt.Errorf("检索相似笔记失败: %w", queryErr)
	}

	log.C(ctx).Infow("检索到相似笔记", "user_id", userID, "count", len(results), "target_book_ids", bookIDs)

	// 4. 内存过滤：筛选出属于当前用户且属于指定 bookIDs 的笔记，按相似度排序取 Top 3
	var filteredBooks []chromem.Result
	for _, result := range results {
		// 验证用户ID
		if userIDFromMeta, ok := result.Metadata["user_id"]; !ok || userIDFromMeta != userIDStr {
			continue
		}
		// 验证笔记ID是否在指定的 bookIDs 中
		if bookIDFromMeta, ok := result.Metadata["book_id"]; ok && bookIDSet[bookIDFromMeta] {
			filteredBooks = append(filteredBooks, result)
			if len(filteredBooks) >= 3 {
				break
			}
		}
	}

	log.C(ctx).Infow("过滤后的笔记", "user_id", userID, "count", len(filteredBooks), "book_ids", bookIDs)

	// 5. Prompt 组装：将筛选出的笔记内容拼接成 System Prompt
	var contextText string
	if len(filteredBooks) == 0 {
		contextText = "未找到相关笔记内容。"
	} else {
		contextText = "基于以下笔记回答用户问题：\n\n"
		for i, book := range filteredBooks {
			content := book.Metadata["content"]
			if content == "" {
				content = book.Content
			}
			contextText += fmt.Sprintf("【笔记 %d】\n%s\n\n", i+1, content)
		}
	}

	// 6. 调用 LLM：构造 messages，使用流式API
	var systemPrompt string
	if deepThinking {
		// 深度思考模式：要求更深入的分析和思考
		systemPrompt = fmt.Sprintf(`你是一位智能助手，专门帮助用户基于他们创建的笔记内容回答问题。当前处于深度思考模式，请进行更深入的分析和思考。

%s

## 用户问题
%s

## 回答要求（深度思考模式）
1. 基于上述笔记内容进行深入分析和思考，回答用户的问题
2. 如果笔记中包含相关信息，请深入挖掘这些信息，提供更全面的分析
3. 如果笔记中没有相关信息，可以基于你的知识回答，但要说明这是基于通用知识
4. 回答要深入、全面、有洞察力，不仅回答表面问题，还要提供更深层的思考
5. 可以适当展开相关背景、原因、影响等方面的分析
6. 使用中文回答

请直接回答用户的问题，不要包含"根据上下文"等前缀。`, contextText, question)
	} else {
		// 普通模式：简洁回答
		systemPrompt = fmt.Sprintf(`你是一位智能助手，专门帮助用户基于他们创建的笔记内容回答问题。

%s

## 用户问题
%s

## 回答要求
1. 基于上述笔记内容回答用户的问题
2. 如果笔记中包含相关信息，请优先使用这些信息
3. 如果笔记中没有相关信息，可以基于你的知识回答，但要说明这是基于通用知识
4. 回答要准确、简洁、有帮助
5. 使用中文回答

请直接回答用户的问题，不要包含"根据上下文"等前缀。`, contextText, question)
	}

	messages := []map[string]string{
		{"role": "user", "content": systemPrompt},
	}

	log.C(ctx).Infow("开始调用LLM生成流式回答", "user_id", userID, "deep_thinking", deepThinking)

	// 使用流式API（参考 generator.go 中的实现）
	err = r.callAliStream(ctx, messages, deepThinking, handler)
	if err != nil {
		log.C(ctx).Errorw("LLM生成流式回答失败", "error", err, "user_id", userID)
		return fmt.Errorf("LLM生成流式回答失败: %w", err)
	}

	log.C(ctx).Infow("RAG流式对话完成", "user_id", userID)
	return nil
}

// callAliStream 调用阿里百炼流式API（内部方法）
// deepThinking: 是否开启深度思考模式，影响 temperature 参数
func (r *RagService) callAliStream(ctx context.Context, messages []map[string]string, deepThinking bool, handler func(chunk string) error) error {
	url := "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"

	model := r.getAliModel(ctx)
	apiKey := r.getAliAPIKey(ctx)

	if apiKey == "" {
		return fmt.Errorf("阿里API密钥未配置")
	}

	// 根据深度思考模式调整参数
	temperature := 0.7 // 默认温度
	if deepThinking {
		// 深度思考模式：降低温度，使回答更稳定、更深入
		temperature = 0.3
	}

	bodyMap := map[string]interface{}{
		"model":       model,
		"messages":    messages,
		"max_tokens":  4000,
		"temperature": temperature,
		"stream":      true,
	}

	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	log.C(ctx).Infow("调用阿里API", "model", model, "api_key_prefix", apiKey[:20]+"...")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP错误: %d", resp.StatusCode)
	}

	// 流式读取响应
	scanner := bufio.NewScanner(resp.Body)
	chunkCount := 0
	for scanner.Scan() {
		line := scanner.Text()

		// 过滤 SSE 注释行（以 : 开头）和空行，防止心跳等注释内容混入输出
		if strings.HasPrefix(line, ":") || line == "" {
			continue
		}

		log.C(ctx).Infow("收到LLM响应行", "line", line)
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				log.C(ctx).Infow("LLM流式响应完成", "chunk_count", chunkCount)
				break
			}

			var m map[string]interface{}
			if err := json.Unmarshal([]byte(data), &m); err == nil {
				if choices, ok := m["choices"].([]interface{}); ok && len(choices) > 0 {
					if choice, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := choice["delta"].(map[string]interface{}); ok {
							if content, ok := delta["content"].(string); ok && content != "" {
								chunkCount++
								log.C(ctx).Infow("调用handler处理chunk", "chunk_num", chunkCount, "content_length", len(content))
								if err := handler(content); err != nil {
									log.C(ctx).Errorw("handler处理失败", "error", err)
									return err
								}
								log.C(ctx).Infow("handler处理成功", "chunk_num", chunkCount)
							}
						}
					}
				}
			}
		}
	}

	if chunkCount == 0 {
		log.C(ctx).Warnw("未收到任何chunk，可能是非流式响应")
	}

	return scanner.Err()
}

// getAliModel 获取阿里模型名称（优先级：Redis → MySQL → Viper）
func (r *RagService) getAliModel(ctx context.Context) string {
	if r.configReader != nil {
		model := r.configReader.GetString(ctx, "ali.text.model")
		if model != "" {
			return model
		}
	}
	// 降级到 viper
	model := viper.GetString("ali.text.model")
	if model == "" {
		return "qwen-turbo" // 默认值
	}
	return model
}

// getAliAPIKey 获取阿里API密钥（优先级：Redis → MySQL → Viper）
func (r *RagService) getAliAPIKey(ctx context.Context) string {
	var apiKey string

	// 优先级1: 从 ConfigReader 读取（会自动从 Redis → MySQL → Viper 读取）
	if r.configReader != nil {
		apiKey = r.configReader.GetString(ctx, "ali.api_key")
		if apiKey != "" {
			log.C(ctx).Debugw("使用统一配置的阿里API密钥", "source", "config_reader")
			return apiKey
		}

		// 尝试从文本服务专用密钥
		apiKey = r.configReader.GetString(ctx, "ali.text.api_key")
		if apiKey != "" {
			log.C(ctx).Debugw("使用文本服务专用API密钥", "source", "config_reader")
			return apiKey
		}
	}

	// 优先级2: 从 viper 读取（兼容性）
	apiKey = viper.GetString("ali.api_key")
	if apiKey != "" {
		log.C(ctx).Debugw("使用统一配置的阿里API密钥", "source", "viper")
		return apiKey
	}

	apiKey = viper.GetString("ali.text.api_key")
	if apiKey != "" {
		log.C(ctx).Debugw("使用文本服务专用API密钥", "source", "viper")
		return apiKey
	}

	log.C(ctx).Errorw("阿里API密钥未配置", "checked_keys", []string{"ali.api_key", "ali.text.api_key"})
	return ""
}

// AddBookVector 添加笔记向量到向量数据库（异步调用）
// userID: 用户ID，用于数据隔离
// bookID: 笔记ID
// content: 笔记内容（优先使用 ProcessedText，如果为空则使用 OriginalText）
func (r *RagService) AddBookVector(ctx context.Context, userID uint, bookID uint, content string) error {
	if content == "" {
		log.C(ctx).Warnw("笔记内容为空，跳过向量化", "user_id", userID, "book_id", bookID)
		return nil // 内容为空时不报错，只是跳过
	}

	// 调用阿里百炼 Embedding API 获取向量
	log.C(ctx).Infow("开始生成笔记向量", "user_id", userID, "book_id", bookID, "content_length", len(content))
	embedding, err := r.aliBiz.QianwenEmbedding(content)
	if err != nil {
		log.C(ctx).Errorw("生成笔记向量失败", "error", err, "user_id", userID, "book_id", bookID)
		return fmt.Errorf("生成笔记向量失败: %w", err)
	}

	log.C(ctx).Infow("笔记向量生成成功", "user_id", userID, "book_id", bookID, "embedding_dim", len(embedding))

	// 将向量存入 chromem
	// 使用 bookID 作为文档ID，在 Metadata 中记录 user_id 以便隔离数据
	docID := fmt.Sprintf("book_%d", bookID)
	metadata := map[string]string{
		"user_id": strconv.FormatUint(uint64(userID), 10),
		"book_id": strconv.FormatUint(uint64(bookID), 10),
		"content": content,
	}

	// 创建文档（Document.Embedding 是 []float32，直接使用）
	doc := chromem.Document{
		ID:        docID,
		Embedding: embedding, // chromem-go 使用 []float32
		Metadata:  metadata,
		Content:   content,
	}

	// 先删除旧文档（如果存在），然后添加新文档
	_ = r.collection.Delete(ctx, nil, nil, docID)

	// 添加文档
	err = r.collection.AddDocuments(ctx, []chromem.Document{doc}, 1)
	if err != nil {
		log.C(ctx).Errorw("存储笔记向量失败", "error", err, "user_id", userID, "book_id", bookID)
		return fmt.Errorf("存储笔记向量失败: %w", err)
	}

	log.C(ctx).Infow("笔记向量存储成功", "user_id", userID, "book_id", bookID, "doc_id", docID)
	return nil
}

// UpdateBookVector 更新笔记向量（先删除再添加）
func (r *RagService) UpdateBookVector(ctx context.Context, userID uint, bookID uint, content string) error {
	// 先删除旧向量
	if err := r.DeleteBookVector(ctx, bookID); err != nil {
		// 如果删除失败（可能笔记不存在），继续执行添加操作
		log.C(ctx).Warnw("删除旧笔记向量失败，继续添加", "error", err, "book_id", bookID)
	}
	// 添加新向量
	return r.AddBookVector(ctx, userID, bookID, content)
}

// DeleteBookVector 删除笔记向量（从向量数据库中移除）
func (r *RagService) DeleteBookVector(ctx context.Context, bookID uint) error {
	docID := fmt.Sprintf("book_%d", bookID)
	err := r.collection.Delete(ctx, nil, nil, docID)
	if err != nil {
		log.C(ctx).Warnw("删除笔记向量失败", "error", err, "book_id", bookID)
		// 删除失败不报错，可能向量不存在
		return nil
	}
	log.C(ctx).Infow("笔记向量删除成功", "book_id", bookID, "doc_id", docID)
	return nil
}

// CheckBookVectorExists 检查笔记向量是否存在
func (r *RagService) CheckBookVectorExists(ctx context.Context, bookID uint) (bool, error) {
	docID := fmt.Sprintf("book_%d", bookID)
	_, err := r.collection.GetByID(ctx, docID)
	if err != nil {
		// 如果获取失败，说明向量不存在
		return false, nil
	}
	return true, nil
}
