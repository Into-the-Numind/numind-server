package book

import (
	"context"
	"sort"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// SearchService 书籍搜索服务
type SearchService struct {
	keywordMatcher   *util.KeywordMatcher
	keywordGenerator *KeywordGenerator
}

// NewSearchService 创建新的搜索服务
func NewSearchService() *SearchService {
	return &SearchService{
		keywordMatcher:   util.NewKeywordMatcher(),
		keywordGenerator: NewKeywordGenerator(),
	}
}

// Close 关闭搜索服务
func (s *SearchService) Close() {
	if s.keywordMatcher != nil {
		s.keywordMatcher.Close()
	}
	if s.keywordGenerator != nil {
		s.keywordGenerator.Close()
	}
}

// SearchBooks 根据用户查询搜索书籍
func (s *SearchService) SearchBooks(ctx context.Context, userQuery string, books []*model.BookM, limit int) []*model.BookM {
	if userQuery == "" || len(books) == 0 {
		return books
	}

	log.C(ctx).Infow("Starting book search", "query", userQuery, "total_books", len(books))

	// 获取用户查询的关键词
	userKeywords := s.keywordMatcher.GetKeywords(userQuery)
	log.C(ctx).Infow("Extracted user keywords", "keywords", userKeywords)

	if len(userKeywords) == 0 {
		log.C(ctx).Warnw("No keywords extracted from user query", "query", userQuery)
		return books
	}

	// 为每本书计算匹配分数
	type bookScore struct {
		book  *model.BookM
		score int
	}

	var bookScores []bookScore
	for _, book := range books {
		// 使用新的MatchScore方法，传入BookMatcher接口
		score := s.keywordMatcher.MatchScore(userKeywords, book)
		bookScores = append(bookScores, bookScore{book: book, score: score})

		log.C(ctx).Debugw("Book score calculated",
			"book_id", book.ID,
			"title", book.Title,
			"tags", book.Tags,
			"keywords", book.Keywords,
			"score", score)
	}

	// 按分数降序排序
	sort.Slice(bookScores, func(i, j int) bool {
		return bookScores[i].score > bookScores[j].score
	})

	// 返回前N本最相关的书籍
	var result []*model.BookM
	for i, bs := range bookScores {
		if i >= limit {
			break
		}
		result = append(result, bs.book)
	}

	var topScore int
	if len(bookScores) > 0 {
		topScore = bookScores[0].score
	}

	log.C(ctx).Infow("Book search completed",
		"query", userQuery,
		"total_books", len(books),
		"result_count", len(result),
		"top_score", topScore)

	return result
}

// GetKeywords 获取文本的关键词（供外部调用）
func (s *SearchService) GetKeywords(text string) []string {
	return s.keywordMatcher.GetKeywords(text)
}

// GenerateBookKeywords 为书籍生成关键词（供外部调用）
func (s *SearchService) GenerateBookKeywords(book *model.BookM) []string {
	return s.keywordGenerator.GenerateBookKeywords(book)
}

// UpdateBookKeywords 更新书籍的关键词（供外部调用）
func (s *SearchService) UpdateBookKeywords(book *model.BookM) {
	s.keywordGenerator.UpdateBookKeywords(book)
}

// BatchUpdateKeywords 批量更新书籍关键词（供外部调用）
func (s *SearchService) BatchUpdateKeywords(books []*model.BookM) {
	s.keywordGenerator.BatchUpdateKeywords(books)
}
