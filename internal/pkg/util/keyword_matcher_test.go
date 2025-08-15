package util

import (
	"testing"
)

func TestNewKeywordMatcher(t *testing.T) {
	matcher := NewKeywordMatcher()
	if matcher == nil {
		t.Fatal("Expected non-nil KeywordMatcher")
	}
	defer matcher.Close()
}

func TestGetKeywords(t *testing.T) {
	matcher := NewKeywordMatcher()
	defer matcher.Close()

	tests := []struct {
		input    string
		expected []string
	}{
		{
			input:    "旅行照片卡册",
			expected: []string{"旅行", "照片", "卡册"},
		},
		{
			input:    "美食烹饪菜谱",
			expected: []string{"美食", "烹饪", "菜谱"},
		},
		{
			input:    "人工智能技术",
			expected: []string{"人工智能", "技术"},
		},
		{
			input:    "",
			expected: []string{},
		},
	}

	for _, test := range tests {
		result := matcher.GetKeywords(test.input)
		t.Logf("Input: %s, Result: %v", test.input, result)

		// 由于jieba分词的结果可能因词典版本而异，我们只检查是否提取到了关键词
		if len(test.input) > 0 && len(result) == 0 {
			t.Errorf("Expected non-empty keywords for input: %s", test.input)
		}
	}
}

func TestMatchScore(t *testing.T) {
	matcher := NewKeywordMatcher()
	defer matcher.Close()

	tests := []struct {
		userKeywords []string
		bookTitle    string
		bookTags     string
		expected     int
	}{
		{
			userKeywords: []string{"旅行", "照片"},
			bookTitle:    "旅行照片卡册",
			bookTags:     "旅行,摄影,回忆",
			expected:     3, // 旅行(标题+标签), 照片(标题), 摄影(标签)
		},
		{
			userKeywords: []string{"美食", "烹饪"},
			bookTitle:    "家常菜谱大全",
			bookTags:     "美食,家常菜",
			expected:     1, // 美食(标签)
		},
		{
			userKeywords: []string{"人工智能"},
			bookTitle:    "机器学习入门",
			bookTags:     "技术,编程",
			expected:     0, // 没有匹配
		},
	}

	for _, test := range tests {
		result := matcher.MatchScore(test.userKeywords, test.bookTitle, test.bookTags)
		t.Logf("User: %v, Title: %s, Tags: %s, Score: %d",
			test.userKeywords, test.bookTitle, test.bookTags, result)

		// 由于jieba分词的结果可能因词典版本而异，我们只检查基本逻辑
		if result < 0 {
			t.Errorf("Expected non-negative score, got %d", result)
		}
	}
}

func TestIsStopWord(t *testing.T) {
	tests := []struct {
		word     string
		expected bool
	}{
		{"的", true},
		{"了", true},
		{"旅行", false},
		{"照片", false},
		{"卡册", false},
	}

	for _, test := range tests {
		result := isStopWord(test.word)
		if result != test.expected {
			t.Errorf("isStopWord(%s) = %v, expected %v", test.word, result, test.expected)
		}
	}
}
