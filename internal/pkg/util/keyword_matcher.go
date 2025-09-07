package util

import (
	"strings"

	"github.com/go-ego/gse"
	"github.com/spf13/viper"
)

// KeywordMatcher 关键词匹配器
type KeywordMatcher struct {
	segmenter gse.Segmenter
}

// NewKeywordMatcher 创建新的关键词匹配器
func NewKeywordMatcher() *KeywordMatcher {
	// 检查是否启用GSE分词功能
	if !isGSEEnabled() {
		// 如果未启用GSE，返回一个空的匹配器
		return &KeywordMatcher{
			segmenter: gse.Segmenter{},
		}
	}

	// 创建 gse 分词器并加载默认词典
	seg, err := gse.New("zh", "dict")
	if err != nil {
		// 如果创建失败，返回一个空的匹配器
		return &KeywordMatcher{
			segmenter: gse.Segmenter{}, // Changed from nil to gse.Segmenter{}
		}
	}
	seg.LoadDict()

	return &KeywordMatcher{
		segmenter: seg,
	}
}

// isGSEEnabled 检查是否启用GSE分词功能
func isGSEEnabled() bool {
	// 从配置文件读取是否启用GSE
	// 默认返回false，避免每次启动都加载字典
	return viper.GetBool("gse.enabled")
}

// Close 关闭分词器，释放资源
func (km *KeywordMatcher) Close() {
	// gse 不需要显式关闭，但保留接口一致性
}

// GetKeywords 从文本中提取关键词
func (km *KeywordMatcher) GetKeywords(text string) []string {
	if text == "" {
		return []string{}
	}

	// 使用 gse 进行分词
	segments := km.segmenter.Cut(text, true)

	// 过滤和清理关键词
	var keywords []string
	for _, word := range segments {
		word = strings.TrimSpace(word)
		if word == "" {
			continue
		}

		// 过滤停用词和单字符
		if !km.isStopWord(word) && len(word) > 1 {
			keywords = append(keywords, word)
		}
	}

	return keywords
}

// MatchScore 计算用户关键词与书籍的匹配分数
func (km *KeywordMatcher) MatchScore(userKeywords []string, book BookMatcher) float64 {
	if len(userKeywords) == 0 {
		return 0.0
	}

	// 获取书籍的关键词（优先使用自动生成的关键词）
	bookKeywords := book.GetKeywords()
	if len(bookKeywords) == 0 {
		// 如果没有自动生成的关键词，则从标题和标签中提取
		title := book.GetTitle()
		tags := book.GetTags()

		if title != "" {
			bookKeywords = append(bookKeywords, km.GetKeywords(title)...)
		}
		if tags != "" {
			bookKeywords = append(bookKeywords, km.GetKeywords(tags)...)
		}
	}

	if len(bookKeywords) == 0 {
		return 0.0
	}

	// 计算关键词匹配度
	matchedCount := 0
	for _, userKeyword := range userKeywords {
		for _, bookKeyword := range bookKeywords {
			if strings.Contains(bookKeyword, userKeyword) || strings.Contains(userKeyword, bookKeyword) {
				matchedCount++
				break
			}
		}
	}

	// 计算匹配分数：匹配的关键词数量 / 用户关键词总数
	score := float64(matchedCount) / float64(len(userKeywords))

	// 额外加分：如果书籍有自动生成的关键词，给予额外权重
	if len(book.GetKeywords()) > 0 {
		score += 0.1
	}

	return score
}

// isStopWord 判断是否为停用词
func (km *KeywordMatcher) isStopWord(word string) bool {
	// 简单的停用词列表
	stopWords := map[string]bool{
		"的": true, "了": true, "在": true, "是": true, "我": true, "有": true, "和": true, "就": true,
		"不": true, "人": true, "都": true, "一": true, "一个": true, "上": true, "也": true, "很": true,
		"到": true, "说": true, "要": true, "去": true, "你": true, "会": true, "着": true, "没有": true,
		"看": true, "好": true, "自己": true, "这": true, "那": true, "什么": true, "怎么": true, "为什么": true,
		"可以": true, "应该": true, "需要": true, "想要": true, "希望": true, "觉得": true, "认为": true,
		"因为": true, "所以": true, "但是": true, "如果": true, "虽然": true, "然后": true, "现在": true,
		"已经": true, "还是": true, "只是": true, "里": true, "来": true, "对": true, "能": true, "下": true,
		"过": true, "还": true, "小": true, "大": true, "多": true, "少": true, "只": true, "哪里": true,
		"什么时候": true, "怎么样": true,
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
		score float64
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
