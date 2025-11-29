package book

import (
	"context"
	"strings"

	"numind-server/internal/pkg/model"
)

// SearchService 搜索服务（已简化，不再使用 gse 分词）
type SearchService struct {
}

// NewSearchService 创建新的搜索服务
func NewSearchService() *SearchService {
	return &SearchService{}
}

// Close 关闭搜索服务（空实现，保持接口兼容）
func (s *SearchService) Close() {
}

// SearchBooks 搜索书籍（简化版本，使用简单的字符串匹配）
func (s *SearchService) SearchBooks(ctx context.Context, userQuery string, books []*model.BookM, limit int) []*model.BookM {
	if len(books) == 0 || userQuery == "" {
		return []*model.BookM{}
	}

	// 简单的标题和标签匹配（不再使用关键词分词）
	userQueryLower := strings.ToLower(userQuery)
	var result []*model.BookM

	for _, book := range books {
		if len(result) >= limit {
			break
		}

		// 检查标题是否包含查询
		if strings.Contains(strings.ToLower(book.Title), userQueryLower) {
			result = append(result, book)
			continue
		}

		// 检查标签是否包含查询
		if book.Tags != "" && strings.Contains(strings.ToLower(book.Tags), userQueryLower) {
			result = append(result, book)
			continue
		}
	}

	return result
}

// GetKeywords 获取关键词（已废弃，返回空数组）
func (s *SearchService) GetKeywords(text string) []string {
	return []string{}
}

// MatchScore 计算匹配分数（已废弃，返回0）
func (s *SearchService) MatchScore(userKeywords []string, book *model.BookM) float64 {
	return 0.0
}

// GenerateBookKeywords 生成书籍关键词（已废弃，返回空数组）
func (s *SearchService) GenerateBookKeywords(book *model.BookM) []string {
	return []string{}
}

// UpdateBookKeywords 更新书籍关键词（已废弃，空实现）
func (s *SearchService) UpdateBookKeywords(book *model.BookM) {
}

// BatchUpdateKeywords 批量更新关键词（已废弃，空实现）
func (s *SearchService) BatchUpdateKeywords(books []*model.BookM) {
}
