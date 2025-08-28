package main

import (
	"fmt"
	"strings"

	"numind-server/internal/numind/biz/markdown"
)

func main() {
	fmt.Println("🔍 测试book创建中的分页逻辑")
	fmt.Println("==================================================")

	// 创建HTML转换器
	htmlConverter := markdown.NewHTMLConverter()

	// 测试数据 - 您提供的内容
	testMarkdown := `阅读是一项重要的技能，它不仅能够丰富我们的知识，还能提升我们的思维能力、语言表达能力和情感体验。
阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。首先，它有助于扩展知识面。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。例如，阅读历史书籍可以帮助我们了解过去发生的事件及其影响，而阅读科学类书籍则能让我们掌握最新的研究成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。`

	fmt.Println("📄 测试Markdown分页...")
	cards, err := htmlConverter.SplitContentByHeight(testMarkdown)
	if err != nil {
		fmt.Printf("❌ 分页失败: %v\n", err)
		return
	}

	fmt.Printf("✅ 分页完成，共生成 %d 张卡片\n\n", len(cards))

	// 分析每张卡片
	for i, card := range cards {
		fmt.Printf("📄 卡片 %d:\n", i+1)
		fmt.Printf("   内容长度: %d 字符\n", len(card))
		fmt.Printf("   行数: %d\n", len(strings.Split(card, "\n")))

		// 检查是否包含关键内容
		keyPhrases := []string{
			"成果和技术发展",
			"阅读还可以提高我们的学习能力",
			"大脑需要不断处理和分析信息",
			"锻炼我们的逻辑思维能力和记忆力",
			"阅读对个人成长有着深远的影响",
		}

		fmt.Printf("   包含关键短语:\n")
		for _, phrase := range keyPhrases {
			if strings.Contains(card, phrase) {
				fmt.Printf("     ✅ %s\n", phrase)
			} else {
				fmt.Printf("     ❌ %s\n", phrase)
			}
		}

		fmt.Printf("   内容预览: %s\n", truncateString(card, 200))
		fmt.Println()
	}

	// 检查目标内容是否被正确分页
	targetContent := "成果和技术发展。此外，阅读还可以提高我们的学习能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。阅读对个人成长有着深远的影响。"

	fmt.Printf("🔍 查找目标内容: %s\n", truncateString(targetContent, 100))
	fmt.Println("==================================================")

	found := false
	for i, card := range cards {
		if strings.Contains(card, "成果和技术发展") {
			found = true
			fmt.Printf("✅ 在第 %d 张卡片中找到目标内容\n", i+1)
			fmt.Printf("📄 卡片 %d 详细内容:\n", i+1)
			fmt.Println("==================================================")
			fmt.Println(card)
			fmt.Println("==================================================")
			break
		}
	}

	if !found {
		fmt.Println("❌ 在所有卡片中都没有找到目标内容")
	}

	// 统计内容分布
	fmt.Println("\n📊 内容分布统计:")
	totalContent := len(testMarkdown)
	totalCardContent := 0
	for i, card := range cards {
		cardLength := len(card)
		totalCardContent += cardLength
		percentage := float64(cardLength) / float64(totalContent) * 100
		fmt.Printf("   卡片 %d: %d 字符 (%.1f%%)\n", i+1, cardLength, percentage)
	}

	coveragePercentage := float64(totalCardContent) / float64(totalContent) * 100
	fmt.Printf("   总覆盖率: %.1f%% (%d/%d 字符)\n", coveragePercentage, totalCardContent, totalContent)

	// 验证分页效果
	if len(cards) >= 2 {
		fmt.Println("\n✅ 验证通过：内容被正确分页为多张卡片")
	} else {
		fmt.Println("\n❌ 验证失败：内容应该被分页为多张卡片，但只生成了", len(cards), "张")
	}

	fmt.Println("\n🎉 测试完成！")
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
