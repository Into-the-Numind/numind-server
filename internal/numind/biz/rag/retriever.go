package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// NoteContent 笔记内容结构
type NoteContent struct {
	BookID    uint
	BookTitle string
	Content   string // 提取的文本内容
}

// RetrieveNotes 检索用户的相关笔记内容
// bookID: 如果指定了bookID，优先检索该笔记；如果为0，则检索所有相关笔记
func RetrieveNotes(ctx context.Context, ds store.IStore, userID uint, query string, bookID uint, limit int) ([]NoteContent, error) {
	var results []NoteContent

	queryLower := strings.ToLower(query)
	queryWords := extractWords(query)

	// 如果指定了bookID，优先检索该笔记
	if bookID > 0 {
		log.C(ctx).Infow("检索指定笔记", "book_id", bookID, "user_id", userID)
		book, err := ds.Books().GetByID(ctx, bookID)
		if err != nil {
			log.C(ctx).Errorw("获取指定笔记失败", "book_id", bookID, "error", err)
			return nil, fmt.Errorf("获取指定笔记失败: %w", err)
		}

		// 验证笔记属于该用户
		if book.UserID != userID {
			log.C(ctx).Warnw("无权访问该笔记", "book_id", bookID, "book_user_id", book.UserID, "request_user_id", userID)
			return nil, fmt.Errorf("无权访问该笔记")
		}

		// 验证笔记状态
		if book.Status != model.BookStatusSuccess {
			log.C(ctx).Warnw("笔记状态不是success", "book_id", bookID, "status", book.Status)
			return nil, fmt.Errorf("笔记状态不是success，无法检索")
		}

		// 提取笔记文本内容
		content := extractBookText(book)
		log.C(ctx).Infow("提取笔记内容",
			"book_id", bookID,
			"content_length", len(content),
			"has_processed_text", book.ProcessedText != "",
			"processed_text_length", len(book.ProcessedText),
			"has_original_text", book.OriginalText != "",
			"original_text_length", len(book.OriginalText),
			"status", book.Status)

		if content == "" {
			log.C(ctx).Warnw("笔记内容为空，无法检索",
				"book_id", bookID,
				"processed_text_empty", book.ProcessedText == "",
				"original_text_empty", book.OriginalText == "")
			// 如果内容为空，返回空结果而不是错误，让AI知道没有找到内容
			return []NoteContent{}, nil
		}

		// 如果指定了bookID，即使匹配分数为0，也返回该笔记的内容
		// 提取前2000个字符作为内容片段（指定笔记时返回更多内容）
		snippet := content
		if len(snippet) > 2000 {
			snippet = snippet[:2000] + "..."
		}

		results = append(results, NoteContent{
			BookID:    book.ID,
			BookTitle: book.Title,
			Content:   snippet,
		})

		return results, nil
	}

	// 如果没有指定bookID，检索所有相关笔记
	// 1. 获取用户的所有笔记（只获取状态为success的）
	_, books, err := ds.Books().ListByUser(ctx, userID, 0, 1000)
	if err != nil {
		return nil, fmt.Errorf("获取笔记列表失败: %w", err)
	}

	// 2. 遍历笔记，提取相关内容
	for _, book := range books {
		if book.Status != model.BookStatusSuccess {
			continue
		}

		// 提取笔记文本内容
		content := extractBookText(book)
		if content == "" {
			continue
		}

		// 简单的关键词匹配
		contentLower := strings.ToLower(content)
		score := calculateRelevanceScore(queryLower, queryWords, contentLower, book.Title)

		if score > 0 {
			// 提取相关片段（包含查询词的上下文）
			snippet := extractRelevantSnippet(content, query, 500)

			results = append(results, NoteContent{
				BookID:    book.ID,
				BookTitle: book.Title,
				Content:   snippet,
			})
		}
	}

	// 3. 按相关性排序并限制数量
	sort.Slice(results, func(i, j int) bool {
		scoreI := calculateRelevanceScore(queryLower, queryWords, strings.ToLower(results[i].Content), results[i].BookTitle)
		scoreJ := calculateRelevanceScore(queryLower, queryWords, strings.ToLower(results[j].Content), results[j].BookTitle)
		return scoreI > scoreJ
	})

	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

// extractBookText 从BookM中提取文本内容
func extractBookText(book *model.BookM) string {
	// 优先使用ProcessedText（直接使用，不需要格式转换）
	if book.ProcessedText != "" {
		return book.ProcessedText
	}

	// 如果ProcessedText为空，使用OriginalText
	if book.OriginalText != "" {
		return book.OriginalText
	}

	return ""
}

// extractWords 提取查询词
func extractWords(query string) []string {
	words := strings.Fields(query)
	var result []string
	for _, word := range words {
		word = strings.TrimSpace(word)
		if len(word) > 1 {
			result = append(result, strings.ToLower(word))
		}
	}
	return result
}

// calculateRelevanceScore 计算相关性分数
func calculateRelevanceScore(queryLower string, queryWords []string, contentLower, title string) float64 {
	score := 0.0

	// 标题匹配（权重最高）
	if strings.Contains(strings.ToLower(title), queryLower) {
		score += 10.0
	}

	// 完整查询匹配
	if strings.Contains(contentLower, queryLower) {
		score += 8.0
	}

	// 关键词匹配
	for _, word := range queryWords {
		if strings.Contains(contentLower, word) {
			score += 2.0
		}
	}

	return score
}

// extractRelevantSnippet 提取相关文本片段
func extractRelevantSnippet(text, query string, maxLength int) string {
	queryLower := strings.ToLower(query)
	textLower := strings.ToLower(text)

	// 找到查询词的位置
	idx := strings.Index(textLower, queryLower)
	if idx == -1 {
		// 如果找不到完整匹配，返回开头
		if len(text) > maxLength {
			return text[:maxLength] + "..."
		}
		return text
	}

	// 提取包含查询词的片段
	start := idx - maxLength/2
	if start < 0 {
		start = 0
	}

	end := idx + len(query) + maxLength/2
	if end > len(text) {
		end = len(text)
	}

	snippet := text[start:end]
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(text) {
		snippet = snippet + "..."
	}

	return snippet
}
