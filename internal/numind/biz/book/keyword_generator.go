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
	matcher := util.NewKeywordMatcher()
	return &KeywordGenerator{
		keywordMatcher: matcher,
	}
}

// Close 关闭关键词生成器
func (kg *KeywordGenerator) Close() {
	if kg.keywordMatcher != nil {
		kg.keywordMatcher.Close()
	}
}

// GenerateBookKeywords 为书籍自动生成关键词
func (kg *KeywordGenerator) GenerateBookKeywords(book *model.BookM) []string {
	if book == nil || book.Title == "" {
		return []string{}
	}

	// 使用jieba分词器对标题进行分词
	keywords := kg.keywordMatcher.GetKeywords(book.Title)

	// 如果书籍有标签，也从标签中提取关键词
	if book.Tags != "" {
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

// GenerateKeywordsFromText 从文本生成关键词
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

	keywordMap := make(map[string]bool)
	var uniqueKeywords []string

	for _, keyword := range keywords {
		if !keywordMap[keyword] {
			keywordMap[keyword] = true
			uniqueKeywords = append(uniqueKeywords, keyword)
		}
	}

	return uniqueKeywords
}

// UpdateBookKeywords 更新书籍的关键词
func (kg *KeywordGenerator) UpdateBookKeywords(book *model.BookM) {
	if book == nil {
		return
	}

	// 生成新的关键词
	keywords := kg.GenerateBookKeywords(book)
	
	// 使用SetKeywords方法，会自动同步更新KeywordsText字段
	book.SetKeywords(keywords)
}

// BatchUpdateKeywords 批量更新书籍关键词
func (kg *KeywordGenerator) BatchUpdateKeywords(books []*model.BookM) {
	for _, book := range books {
		kg.UpdateBookKeywords(book)
	}
}

// GetKeywordsForMatching 获取用于匹配的关键词
func (kg *KeywordGenerator) GetKeywordsForMatching(book *model.BookM) []string {
	if book == nil {
		return []string{}
	}

	// 优先使用已生成的关键词
	if len(book.Keywords) > 0 {
		return book.Keywords
	}

	// 如果没有生成的关键词，则实时生成
	return kg.GenerateBookKeywords(book)
}
