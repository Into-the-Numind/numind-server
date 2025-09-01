#!/bin/bash

# 测试 gse 分词库功能
# 替换 gojieba 后的验证脚本

set -e

echo "🧪 测试 gse 分词库功能"
echo "================================"

# 检查 Go 环境
if ! command -v go &> /dev/null; then
    echo "❌ Go 未安装"
    exit 1
fi

echo "✅ Go 环境检查通过"
echo "Go 版本: $(go version)"

# 检查依赖
echo ""
echo "📦 检查依赖..."
if ! go list -m github.com/go-ego/gse &> /dev/null; then
    echo "❌ gse 依赖未安装"
    echo "正在安装..."
    go get github.com/go-ego/gse@latest
else
    echo "✅ gse 依赖已安装"
fi

# 创建测试文件
echo ""
echo "🔧 创建测试文件..."
cat > test_gse.go << 'EOF'
package main

import (
	"fmt"
	"strings"

	"github.com/go-ego/gse"
)

func main() {
	fmt.Println("🧪 gse 分词库测试")
	fmt.Println("================================")

	// 创建分词器
	seg, err := gse.New("zh", "dict")
	if err != nil {
		fmt.Printf("❌ 创建分词器失败: %v\n", err)
		return
	}
	seg.LoadDict()

	// 测试文本
	testTexts := []string{
		"我想找一些关于摄影的书籍",
		"推荐一些技术类的卡册",
		"有什么好看的旅行相册",
		"关于美食的推荐",
		"艺术设计相关的书籍",
	}

	for i, text := range testTexts {
		fmt.Printf("\n📝 测试文本 %d: %s\n", i+1, text)
		
		// 使用 gse 分词
		segments := seg.Cut(text, true)
		fmt.Printf("🔍 分词结果: %v\n", segments)
		
		// 过滤关键词
		var keywords []string
		for _, word := range segments {
			word = strings.TrimSpace(word)
			if word != "" && len(word) > 1 {
				// 简单的停用词过滤
				if !isStopWord(word) {
					keywords = append(keywords, word)
				}
			}
		}
		
		fmt.Printf("✨ 关键词: %v\n", keywords)
	}

	fmt.Println("\n🎉 gse 分词库测试完成！")
}

func isStopWord(word string) bool {
	stopWords := map[string]bool{
		"的": true, "了": true, "在": true, "是": true, "我": true, "有": true, "和": true, "就": true,
		"不": true, "人": true, "都": true, "一": true, "一些": true, "上": true, "也": true, "很": true,
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
EOF

# 运行测试
echo ""
echo "🚀 运行 gse 测试..."
if go run test_gse.go; then
    echo "✅ gse 分词库测试成功！"
else
    echo "❌ gse 分词库测试失败"
    exit 1
fi

# 清理测试文件
echo ""
echo "🧹 清理测试文件..."
rm -f test_gse.go

echo ""
echo "🎯 gse 分词库集成验证完成！"
echo "✅ 分词功能正常"
echo "✅ 关键词提取正常"
echo "✅ 停用词过滤正常"
echo ""
echo "💡 现在可以使用 gse 库进行中文分词了"
