package book

import (
	"context"
	"sort"

	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// SearchService 搜索服务
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

// SearchBooks 搜索书籍
func (s *SearchService) SearchBooks(ctx context.Context, userQuery string, books []*model.BookM, limit int) []*model.BookM {
	if len(books) == 0 || userQuery == "" {
		return []*model.BookM{}
	}

	// 从用户查询中提取关键词
	userKeywords := s.keywordMatcher.GetKeywords(userQuery)
	if len(userKeywords) == 0 {
		return []*model.BookM{}
	}

	// 计算每本书的匹配分数
	type bookScore struct {
		book  *model.BookM
		score float64
	}

	var bookScores []bookScore
	for _, book := range books {
		// 确保书籍有关键词
		if len(book.Keywords) == 0 {
			s.keywordGenerator.UpdateBookKeywords(book)
		}
		
		score := s.keywordMatcher.MatchScore(userKeywords, book)
		bookScores = append(bookScores, bookScore{book: book, score: score})
	}

	// 按分数降序排序
	sort.Slice(bookScores, func(i, j int) bool {
		return bookScores[i].score > bookScores[j].score
	})

	// 返回前limit本书
	result := make([]*model.BookM, 0, limit)
	for i := 0; i < len(bookScores) && i < limit; i++ {
		if bookScores[i].score > 0 {
			result = append(result, bookScores[i].book)
		}
	}

	return result
}

// GetKeywords 获取关键词（暴露给外部使用）
func (s *SearchService) GetKeywords(text string) []string {
	return s.keywordMatcher.GetKeywords(text)
}

// MatchScore 计算匹配分数（暴露给外部使用）
func (s *SearchService) MatchScore(userKeywords []string, book *model.BookM) float64 {
	return s.keywordMatcher.MatchScore(userKeywords, book)
}

// GenerateBookKeywords 生成书籍关键词
func (s *SearchService) GenerateBookKeywords(book *model.BookM) []string {
	return s.keywordGenerator.GenerateBookKeywords(book)
}

// UpdateBookKeywords 更新书籍关键词
func (s *SearchService) UpdateBookKeywords(book *model.BookM) {
	s.keywordGenerator.UpdateBookKeywords(book)
}

// BatchUpdateKeywords 批量更新关键词
func (s *SearchService) BatchUpdateKeywords(books []*model.BookM) {
	s.keywordGenerator.BatchUpdateKeywords(books)
}
