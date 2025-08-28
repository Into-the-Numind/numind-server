package main

import (
	"fmt"
	"log"
	"strings"

	"numind-server/internal/numind/biz/markdown"
)

func main() {
	fmt.Println("🧪 测试跳过第一张卡片的一级标题")
	fmt.Println("==================================================")

	// 创建HTML转换器
	htmlConverter := markdown.NewHTMLConverter()

	// 测试数据 - 包含一级标题的Markdown内容
	testMarkdown := `# 遥远的东方有一条龙，它的名字就叫中国。遥远的东方有一群人，他们都是龙的传人。
阅读是一项重要的技能，它不仅能够丰富我们的知识，还能提升我们的思维能力、语言表达能力和情感体验。

## 阅读的多样性
阅读的多样性体现在不同类型的书籍能够满足不同读者的需求和兴趣。小说类书籍能够提供丰富的故事情节和人物塑造，让读者在阅读过程中获得情感体验和想象力的激发。科普类书籍则能够传递科学知识，帮助读者了解自然规律和科技发展。

### 小说类书籍
小说类书籍是阅读中最受欢迎的类型之一。它们通过虚构的故事情节和人物塑造，为读者提供了一个逃离现实、体验不同人生的机会。无论是古典文学还是现代小说，都能让读者在阅读过程中获得深刻的情感体验和思考。

### 科普类书籍
科普类书籍则专注于传递科学知识，帮助读者了解自然规律和科技发展。这类书籍通常以通俗易懂的语言解释复杂的科学概念，让普通读者也能理解科学原理和最新研究成果。

## 阅读对思维的影响
阅读不仅能够丰富知识，更重要的是能够提升思维能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。

### 逻辑思维能力的提升
阅读过程中，我们需要理解作者的思路，分析论证过程，这种训练能够显著提升我们的逻辑思维能力。无论是阅读学术论文还是文学作品，都需要我们运用逻辑思维来理解内容。

### 记忆力的锻炼
阅读也是一种很好的记忆力锻炼方式。我们需要记住故事情节、人物关系、重要信息等，这种记忆训练能够帮助我们保持大脑的活跃状态。

## 阅读对语言表达的影响
阅读对语言表达能力的提升也有着重要作用。通过阅读优秀的文学作品，我们可以学习到丰富的词汇、优美的句式、准确的表达方式。这些都有助于提升我们的写作和口语表达能力。

### 词汇量的丰富
阅读是扩充词汇量的最佳方式之一。通过阅读不同类型的书籍，我们可以接触到各种专业术语、文学词汇、日常用语等，从而丰富我们的词汇储备。

### 表达方式的多样化
阅读优秀的作品，我们可以学习到不同的表达方式和写作技巧。这些技巧可以应用到我们自己的写作和表达中，使我们的语言更加生动、准确、有说服力。

## 阅读对情感体验的影响
阅读还能够丰富我们的情感体验。通过阅读文学作品，我们可以体验到不同人物的情感世界，理解人性的复杂性，培养同理心和情感智慧。

### 情感共鸣的培养
优秀的文学作品往往能够引起读者的情感共鸣。通过阅读这些作品，我们可以体验到不同的情感状态，培养对他人情感的理解和同情。

### 人性理解的深化
阅读文学作品，特别是那些深入探讨人性的作品，能够帮助我们更好地理解人性的复杂性，培养更加成熟和深刻的人生观。

## 阅读习惯的养成
要充分发挥阅读的积极作用，我们需要养成良好的阅读习惯。这包括选择合适的阅读时间、创造良好的阅读环境、保持持续的阅读兴趣等。

### 选择合适的阅读时间
每个人的生活节奏不同，需要根据自己的情况选择合适的阅读时间。有些人喜欢在早晨阅读，有些人则更喜欢在晚上睡前阅读。重要的是要找到适合自己的时间，并坚持下去。

### 创造良好的阅读环境
良好的阅读环境能够提高阅读效果。这包括选择安静的地方、保持适当的光线、准备舒适的座椅等。一个良好的阅读环境能够帮助我们更好地专注于阅读内容。

## 阅读的长期价值
阅读的价值不仅体现在短期内的知识获取和技能提升，更重要的是其长期价值。通过持续的阅读，我们可以不断提升自己，实现个人的成长和发展。

### 终身学习的基础
阅读是终身学习的基础。在知识快速更新的今天，我们需要通过持续的阅读来跟上时代的发展，保持自己的竞争力。

### 个人成长的推动力
阅读能够推动个人的成长和发展。通过阅读，我们可以不断拓展视野，提升能力，实现自我价值的提升。

## 总结
阅读是一项重要的技能，它不仅能够丰富我们的知识，还能提升我们的思维能力、语言表达能力和情感体验。通过养成良好的阅读习惯，我们可以充分发挥阅读的积极作用，实现个人的成长和发展。`

	fmt.Println("📄 测试Markdown分页（跳过第一张卡片的一级标题）...")
	cards, err := htmlConverter.SplitContentByHeight(testMarkdown)
	if err != nil {
		log.Fatalf("❌ 分页失败: %v", err)
	}

	fmt.Printf("✅ 分页完成，共生成 %d 张卡片\n\n", len(cards))

	// 分析每张卡片
	for i, card := range cards {
		fmt.Printf("📄 卡片 %d:\n", i+1)
		fmt.Printf("   内容长度: %d 字符\n", len(card))
		fmt.Printf("   行数: %d\n", len(strings.Split(card, "\n")))

		// 检查是否包含一级标题（只检查 # 后面没有 # 的情况）
		hasH1 := false
		lines := strings.Split(card, "\n")
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "# ") && !strings.HasPrefix(strings.TrimSpace(line), "##") {
				hasH1 = true
				break
			}
		}
		fmt.Printf("   包含一级标题: %t\n", hasH1)

		// 检查是否包含二级标题（只检查 ## 后面没有 # 的情况）
		hasH2 := false
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "## ") && !strings.HasPrefix(strings.TrimSpace(line), "###") {
				hasH2 = true
				break
			}
		}
		fmt.Printf("   包含二级标题: %t\n", hasH2)

		// 检查是否包含三级标题
		hasH3 := false
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "### ") {
				hasH3 = true
				break
			}
		}
		fmt.Printf("   包含三级标题: %t\n", hasH3)

		fmt.Printf("   内容预览: %s\n", truncateString(card, 100))
		fmt.Println()
	}

	// 验证第一张卡片是否跳过了一级标题
	if len(cards) > 0 {
		firstCard := cards[0]
		fmt.Println("\n🔍 第一张卡片详细内容:")
		fmt.Println("==================================================")
		fmt.Println(firstCard)
		fmt.Println("==================================================")

		// 检查第一张卡片是否包含一级标题
		hasH1 := false
		lines := strings.Split(firstCard, "\n")
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "# ") && !strings.HasPrefix(strings.TrimSpace(line), "##") {
				hasH1 = true
				break
			}
		}

		if hasH1 {
			fmt.Println("⚠️  警告：第一张卡片仍然包含一级标题")
		} else {
			fmt.Println("✅ 验证通过：第一张卡片已成功跳过一级标题")
		}
	}

	fmt.Println("🎉 测试完成！")
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

import (
	"fmt"
	"log"
	"strings"

	"numind-server/internal/numind/biz/markdown"
)

func main() {
	fmt.Println("🧪 测试跳过第一张卡片的一级标题")
	fmt.Println("==================================================")

	// 创建HTML转换器
	htmlConverter := markdown.NewHTMLConverter()

	// 测试数据 - 包含一级标题的Markdown内容
	testMarkdown := `# 遥远的东方有一条龙，它的名字就叫中国。遥远的东方有一群人，他们都是龙的传人。
阅读是一项重要的技能，它不仅能够丰富我们的知识，还能提升我们的思维能力、语言表达能力和情感体验。

## 阅读的多样性
阅读的多样性体现在不同类型的书籍能够满足不同读者的需求和兴趣。小说类书籍能够提供丰富的故事情节和人物塑造，让读者在阅读过程中获得情感体验和想象力的激发。科普类书籍则能够传递科学知识，帮助读者了解自然规律和科技发展。

### 小说类书籍
小说类书籍是阅读中最受欢迎的类型之一。它们通过虚构的故事情节和人物塑造，为读者提供了一个逃离现实、体验不同人生的机会。无论是古典文学还是现代小说，都能让读者在阅读过程中获得深刻的情感体验和思考。

### 科普类书籍
科普类书籍则专注于传递科学知识，帮助读者了解自然规律和科技发展。这类书籍通常以通俗易懂的语言解释复杂的科学概念，让普通读者也能理解科学原理和最新研究成果。

## 阅读对思维的影响
阅读不仅能够丰富知识，更重要的是能够提升思维能力。当我们阅读时，大脑需要不断处理和分析信息，这种过程可以锻炼我们的逻辑思维能力和记忆力。通过阅读不同类型的书籍，我们可以接触到各种各样的思想和观点，这有助于我们形成更加全面的世界观。

### 逻辑思维能力的提升
阅读过程中，我们需要理解作者的思路，分析论证过程，这种训练能够显著提升我们的逻辑思维能力。无论是阅读学术论文还是文学作品，都需要我们运用逻辑思维来理解内容。

### 记忆力的锻炼
阅读也是一种很好的记忆力锻炼方式。我们需要记住故事情节、人物关系、重要信息等，这种记忆训练能够帮助我们保持大脑的活跃状态。

## 阅读对语言表达的影响
阅读对语言表达能力的提升也有着重要作用。通过阅读优秀的文学作品，我们可以学习到丰富的词汇、优美的句式、准确的表达方式。这些都有助于提升我们的写作和口语表达能力。

### 词汇量的丰富
阅读是扩充词汇量的最佳方式之一。通过阅读不同类型的书籍，我们可以接触到各种专业术语、文学词汇、日常用语等，从而丰富我们的词汇储备。

### 表达方式的多样化
阅读优秀的作品，我们可以学习到不同的表达方式和写作技巧。这些技巧可以应用到我们自己的写作和表达中，使我们的语言更加生动、准确、有说服力。

## 阅读对情感体验的影响
阅读还能够丰富我们的情感体验。通过阅读文学作品，我们可以体验到不同人物的情感世界，理解人性的复杂性，培养同理心和情感智慧。

### 情感共鸣的培养
优秀的文学作品往往能够引起读者的情感共鸣。通过阅读这些作品，我们可以体验到不同的情感状态，培养对他人情感的理解和同情。

### 人性理解的深化
阅读文学作品，特别是那些深入探讨人性的作品，能够帮助我们更好地理解人性的复杂性，培养更加成熟和深刻的人生观。

## 阅读习惯的养成
要充分发挥阅读的积极作用，我们需要养成良好的阅读习惯。这包括选择合适的阅读时间、创造良好的阅读环境、保持持续的阅读兴趣等。

### 选择合适的阅读时间
每个人的生活节奏不同，需要根据自己的情况选择合适的阅读时间。有些人喜欢在早晨阅读，有些人则更喜欢在晚上睡前阅读。重要的是要找到适合自己的时间，并坚持下去。

### 创造良好的阅读环境
良好的阅读环境能够提高阅读效果。这包括选择安静的地方、保持适当的光线、准备舒适的座椅等。一个良好的阅读环境能够帮助我们更好地专注于阅读内容。

## 阅读的长期价值
阅读的价值不仅体现在短期内的知识获取和技能提升，更重要的是其长期价值。通过持续的阅读，我们可以不断提升自己，实现个人的成长和发展。

### 终身学习的基础
阅读是终身学习的基础。在知识快速更新的今天，我们需要通过持续的阅读来跟上时代的发展，保持自己的竞争力。

### 个人成长的推动力
阅读能够推动个人的成长和发展。通过阅读，我们可以不断拓展视野，提升能力，实现自我价值的提升。

## 总结
阅读是一项重要的技能，它不仅能够丰富我们的知识，还能提升我们的思维能力、语言表达能力和情感体验。通过养成良好的阅读习惯，我们可以充分发挥阅读的积极作用，实现个人的成长和发展。`

	fmt.Println("📄 测试Markdown分页（跳过第一张卡片的一级标题）...")
	cards, err := htmlConverter.SplitContentByHeight(testMarkdown)
	if err != nil {
		log.Fatalf("❌ 分页失败: %v", err)
	}

	fmt.Printf("✅ 分页完成，共生成 %d 张卡片\n\n", len(cards))

	// 分析每张卡片
	for i, card := range cards {
		fmt.Printf("📄 卡片 %d:\n", i+1)
		fmt.Printf("   内容长度: %d 字符\n", len(card))
		fmt.Printf("   行数: %d\n", len(strings.Split(card, "\n")))

		// 检查是否包含一级标题（只检查 # 后面没有 # 的情况）
		hasH1 := false
		lines := strings.Split(card, "\n")
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "# ") && !strings.HasPrefix(strings.TrimSpace(line), "##") {
				hasH1 = true
				break
			}
		}
		fmt.Printf("   包含一级标题: %t\n", hasH1)

		// 检查是否包含二级标题（只检查 ## 后面没有 # 的情况）
		hasH2 := false
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "## ") && !strings.HasPrefix(strings.TrimSpace(line), "###") {
				hasH2 = true
				break
			}
		}
		fmt.Printf("   包含二级标题: %t\n", hasH2)

		// 检查是否包含三级标题
		hasH3 := false
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "### ") {
				hasH3 = true
				break
			}
		}
		fmt.Printf("   包含三级标题: %t\n", hasH3)

		fmt.Printf("   内容预览: %s\n", truncateString(card, 100))
		fmt.Println()
	}

	// 验证第一张卡片是否跳过了一级标题
	if len(cards) > 0 {
		firstCard := cards[0]
		fmt.Println("\n🔍 第一张卡片详细内容:")
		fmt.Println("==================================================")
		fmt.Println(firstCard)
		fmt.Println("==================================================")

		// 检查第一张卡片是否包含一级标题
		hasH1 := false
		lines := strings.Split(firstCard, "\n")
		for _, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "# ") && !strings.HasPrefix(strings.TrimSpace(line), "##") {
				hasH1 = true
				break
			}
		}

		if hasH1 {
			fmt.Println("⚠️  警告：第一张卡片仍然包含一级标题")
		} else {
			fmt.Println("✅ 验证通过：第一张卡片已成功跳过一级标题")
		}
	}

	fmt.Println("🎉 测试完成！")
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
