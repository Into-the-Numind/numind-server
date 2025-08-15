package util

import (
	"strings"

	"github.com/yanyiwu/gojieba"
)

// KeywordMatcher 关键词匹配器
type KeywordMatcher struct {
	jieba *gojieba.Jieba
}

// NewKeywordMatcher 创建新的关键词匹配器
func NewKeywordMatcher() *KeywordMatcher {
	// 初始化jieba分词器，使用默认词典
	jieba := gojieba.NewJieba()
	return &KeywordMatcher{
		jieba: jieba,
	}
}

// Close 关闭分词器
func (km *KeywordMatcher) Close() {
	if km.jieba != nil {
		km.jieba.Free()
	}
}

// GetKeywords 对给定文本进行分词，返回关键词列表
func (km *KeywordMatcher) GetKeywords(text string) []string {
	if text == "" {
		return []string{}
	}

	// 使用jieba进行分词
	words := km.jieba.Cut(text, true)

	// 过滤和清理关键词
	var keywords []string
	for _, word := range words {
		word = strings.TrimSpace(word)
		// 过滤掉空字符串、单个字符和停用词
		if word != "" && len(word) > 1 && !isStopWord(word) {
			keywords = append(keywords, word)
		}
	}

	return keywords
}

// MatchScore 计算用户关键词与书籍的匹配分数（使用新的Keywords字段）
func (km *KeywordMatcher) MatchScore(userKeywords []string, book BookMatcher) int {
	if len(userKeywords) == 0 {
		return 0
	}

	// 获取书籍的关键词（优先使用自动生成的Keywords字段）
	bookKeywords := book.GetKeywords()

	// 如果Keywords字段为空，则从Title和Tags生成
	if len(bookKeywords) == 0 {
		bookKeywords = km.GetKeywords(book.GetTitle())

		// 如果书籍有标签，也加入关键词中
		if book.GetTags() != "" {
			tagList := strings.Split(book.GetTags(), ",")
			for _, tag := range tagList {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					tagKeywords := km.GetKeywords(tag)
					bookKeywords = append(bookKeywords, tagKeywords...)
				}
			}
		}
	}

	// 计算匹配分数
	score := 0
	userKeywordMap := make(map[string]bool)

	// 将用户关键词放入map中，用于快速查找
	for _, keyword := range userKeywords {
		userKeywordMap[keyword] = true
	}

	// 遍历书籍关键词，计算匹配数
	for _, bookKeyword := range bookKeywords {
		if userKeywordMap[bookKeyword] {
			score++
		}
	}

	return score
}

// isStopWord 判断是否为停用词
func isStopWord(word string) bool {
	// 简单的停用词列表，可以根据需要扩展
	stopWords := map[string]bool{
		"的": true, "了": true, "在": true, "是": true, "我": true, "有": true, "和": true, "就": true,
		"不": true, "人": true, "都": true, "一": true, "一个": true, "上": true, "也": true, "很": true,
		"到": true, "说": true, "要": true, "去": true, "你": true, "会": true, "着": true, "没有": true,
		"看": true, "好": true, "自己": true, "这": true, "那": true, "里": true, "来": true, "对": true,
		"能": true, "下": true, "过": true, "还": true, "小": true, "大": true, "多": true, "少": true,
		"只": true, "可以": true, "因为": true, "所以": true, "但是": true, "如果": true, "然后": true,
		"什么": true, "怎么": true, "为什么": true, "哪里": true, "什么时候": true, "怎么样": true,
	}

	return stopWords[word]
}

// SortBooksByMatchScore 根据匹配分数对书籍进行排序
func (km *KeywordMatcher) SortBooksByMatchScore(userKeywords []string, books []interface{}) {
	// 这里需要根据具体的BookM结构来实现
	// 由于Go的泛型限制，这里提供一个通用的接口
	// 实际使用时需要传入实现了特定接口的书籍切片
}

// BookMatcher 书籍匹配接口
type BookMatcher interface {
	GetTitle() string
	GetTags() string
	GetKeywords() []string
	GetID() uint
}

// SortBooksByMatchScoreGeneric 通用的书籍排序函数
func (km *KeywordMatcher) SortBooksByMatchScoreGeneric(userKeywords []string, books []BookMatcher) []BookMatcher {
	if len(books) == 0 {
		return books
	}

	// 为每本书计算匹配分数
	type bookScore struct {
		book  BookMatcher
		score int
	}

	var bookScores []bookScore
	for _, book := range books {
		score := km.MatchScore(userKeywords, book)
		bookScores = append(bookScores, bookScore{book: book, score: score})
	}

	// 按分数降序排序
	for i := 0; i < len(bookScores)-1; i++ {
		for j := i + 1; j < len(bookScores); j++ {
			if bookScores[i].score < bookScores[j].score {
				bookScores[i], bookScores[j] = bookScores[j], bookScores[i]
			}
		}
	}

	// 返回排序后的书籍
	var sortedBooks []BookMatcher
	for _, bs := range bookScores {
		sortedBooks = append(sortedBooks, bs.book)
	}

	return sortedBooks
}
