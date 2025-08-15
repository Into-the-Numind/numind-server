package book

import (
	"strings"

	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
)

// KeywordGenerator 关键词生成器
type KeywordGenerator struct {
	keywordMatcher *util.KeywordMatcher
}

// NewKeywordGenerator 创建新的关键词生成器
func NewKeywordGenerator() *KeywordGenerator {
	return &KeywordGenerator{
		keywordMatcher: util.NewKeywordMatcher(),
	}
}

// Close 关闭关键词生成器
func (kg *KeywordGenerator) Close() {
	if kg.keywordMatcher != nil {
		kg.keywordMatcher.Close()
	}
}

// GenerateBookKeywords 为书籍生成关键词
func (kg *KeywordGenerator) GenerateBookKeywords(book *model.BookM) []string {
	var keywords []string

	// 从标题生成关键词
	if book.Title != "" {
		titleKeywords := kg.keywordMatcher.GetKeywords(book.Title)
		keywords = append(keywords, titleKeywords...)
	}

	// 从标签生成关键词
	if book.Tags != "" {
		// 分割标签（逗号分隔）
		tagList := strings.Split(book.Tags, ",")
		for _, tag := range tagList {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				tagKeywords := kg.keywordMatcher.GetKeywords(tag)
				keywords = append(keywords, tagKeywords...)
			}
		}
	}

	// 去重并返回
	return kg.deduplicateKeywords(keywords)
}

// GenerateKeywordsFromText 从任意文本生成关键词
func (kg *KeywordGenerator) GenerateKeywordsFromText(text string) []string {
	if text == "" {
		return []string{}
	}
	
	keywords := kg.keywordMatcher.GetKeywords(text)
	return kg.deduplicateKeywords(keywords)
}

// deduplicateKeywords 去重关键词
func (kg *KeywordGenerator) deduplicateKeywords(keywords []string) []string {
	if len(keywords) == 0 {
		return []string{}
	}

	seen := make(map[string]bool)
	var result []string

	for _, keyword := range keywords {
		if !seen[keyword] {
			seen[keyword] = true
			result = append(result, keyword)
		}
	}

	return result
}

// UpdateBookKeywords 更新书籍的关键词
func (kg *KeywordGenerator) UpdateBookKeywords(book *model.BookM) {
	keywords := kg.GenerateBookKeywords(book)
	book.SetKeywords(keywords)
}

// BatchUpdateKeywords 批量更新关键词
func (kg *KeywordGenerator) BatchUpdateKeywords(books []*model.BookM) {
	for _, book := range books {
		kg.UpdateBookKeywords(book)
	}
}

// GetKeywordsForMatching 获取用于匹配的关键词
func (kg *KeywordGenerator) GetKeywordsForMatching(book *model.BookM) []string {
	// 如果已有自动生成的关键词，直接返回
	if len(book.Keywords) > 0 {
		return book.Keywords
	}
	
	// 否则生成新的关键词
	return kg.GenerateBookKeywords(book)
}
