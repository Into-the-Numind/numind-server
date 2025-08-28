package main

import (
	"fmt"
	"log"

	"numind-server/internal/numind/biz/markdown"
)

func main() {
	htmlConverter := markdown.NewHTMLConverter()

	// 测试数据 - 模拟你提供的图片中的内容
	testMarkdown := `阅读是一项重要的技能，它不仅能够丰富我们的知识，还能提升我们的思维能力、语言表达能力和情感体验。

阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。`

	fmt.Println("🔄 测试填充式分页逻辑...")
	fmt.Printf("📄 原始内容长度: %d 字符\n", len(testMarkdown))
	fmt.Println("📄 原始内容预览:")
	fmt.Println(testMarkdown[:200] + "...")
	fmt.Println()

	cards, err := htmlConverter.SplitContentByHeight(testMarkdown)
	if err != nil {
		log.Fatalf("❌ 分页失败: %v", err)
	}

	fmt.Printf("✅ 分页完成，共生成 %d 张卡片\n\n", len(cards))

	for i, card := range cards {
		fmt.Printf("📋 卡片 %d (长度: %d 字符):\n", i+1, len(card))
		fmt.Println("---")
		if len(card) > 300 {
			fmt.Println(card[:300] + "...")
		} else {
			fmt.Println(card)
		}
		fmt.Println("---")
		fmt.Println()
	}

	// 分析分页效果
	fmt.Println("📊 分页效果分析:")
	for i, card := range cards {
		utilization := float64(len(card)) / float64(len(testMarkdown)) * 100
		fmt.Printf("卡片 %d: %d 字符 (%.1f%% 利用率)\n", i+1, len(card), utilization)
	}
}
