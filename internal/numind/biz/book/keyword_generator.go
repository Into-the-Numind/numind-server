package book

import (
	"numind-server/internal/pkg/model"
)

// KeywordGenerator 关键词生成器（已简化，不再使用 gse 分词）
type KeywordGenerator struct {
}

// NewKeywordGenerator 创建新的关键词生成器
func NewKeywordGenerator() *KeywordGenerator {
	return &KeywordGenerator{}
}

// Close 关闭关键词生成器（空实现，保持接口兼容）
func (kg *KeywordGenerator) Close() {
}

// GenerateBookKeywords 为书籍生成关键词（已废弃，返回空数组）
func (kg *KeywordGenerator) GenerateBookKeywords(book *model.BookM) []string {
	return []string{}
}

// GenerateKeywordsFromText 从任意文本生成关键词（已废弃，返回空数组）
func (kg *KeywordGenerator) GenerateKeywordsFromText(text string) []string {
	return []string{}
}

// UpdateBookKeywords 更新书籍的关键词（已废弃，空实现）
func (kg *KeywordGenerator) UpdateBookKeywords(book *model.BookM) {
}

// BatchUpdateKeywords 批量更新关键词（已废弃，空实现）
func (kg *KeywordGenerator) BatchUpdateKeywords(books []*model.BookM) {
}

// GetKeywordsForMatching 获取用于匹配的关键词（已废弃，返回空数组）
func (kg *KeywordGenerator) GetKeywordsForMatching(book *model.BookM) []string {
	return []string{}
}
