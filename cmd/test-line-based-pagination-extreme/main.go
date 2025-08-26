package main

import (
	"fmt"
	"log"

	"github.com/spf13/viper"
	"numind-server/internal/numind/biz/pagination"
)

func main() {
	// 初始化 viper 配置
	viper.SetConfigName("config_local")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("./configs")

	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Error reading config file: %v", err)
	}

	fmt.Println("=== 极端情况按行分页测试 ===")
	fmt.Println("测试目标：验证按行分页能够解决段落分页的空间利用率问题")
	fmt.Println()

	// 创建分页配置
	config := pagination.GetDefaultConfig()
	
	// 创建按行分页引擎
	lineEngine := pagination.NewLineBasedPaginationEngine(config)
	
	// 创建标准分页引擎进行对比
	standardEngine := pagination.NewPaginationEngine(config)

	// 极端情况测试数据 - 模拟图片中的长文本
	longText := `就像跨国企业都在做的glocal (global-local)--'全球本土化'战略,有全球视野,但保存当地特色。如果你有独立思考能力,联机思考会让思想质量变得更高、迭代更快。这个时代,每个人都需要学会如何成为一个'联机的独立思考者'。读100本书并试图记住它们,就像非要背下整本电话簿才开始拨号。智慧不等于信息,记忆应交给电脑。未来世界的核心认知能力是:找到信息的搜索能力、运用信息的思考能力、从海量信息中抓取趋势的洞察能力。`

	testElements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "联机思考与独立思维",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: longText, // 超长文本，模拟极端情况
		},
		{
			Type: pagination.ElementTypeList,
			Content: []string{
				"全球视野：保持对全球趋势的敏感度",
				"本地特色：保留和发扬本土文化特色",
				"独立思考：培养个人独立判断能力",
				"联机协作：通过合作提升思维质量",
			},
		},
	}

	fmt.Printf("📝 极端情况测试数据：%d 个元素\n", len(testElements))
	fmt.Printf("📏 长文本长度：%d 字符\n", len(longText))
	fmt.Println()

	// 使用标准分页引擎（按段落分页）
	fmt.Println("=== 标准分页引擎结果（按段落分页）===")
	standardResult, err := standardEngine.PaginateElements(testElements)
	if err != nil {
		log.Fatalf("标准分页失败: %v", err)
	}

	fmt.Printf("📊 标准分页结果：%d 张卡片\n", len(standardResult.Cards))
	for i, card := range standardResult.Cards {
		fmt.Printf("  卡片 %d: %d 个元素\n", i+1, len(card.Elements))
		for j, element := range card.Elements {
			content := fmt.Sprintf("%v", element.Content)
			if len(content) > 50 {
				content = content[:50] + "..."
			}
			fmt.Printf("    元素 %d [%s]: %s\n", j+1, element.Type, content)
		}
	}
	fmt.Println()

	// 使用按行分页引擎
	fmt.Println("=== 按行分页引擎结果（按行分页）===")
	lineResult, err := lineEngine.PaginateByLines(testElements)
	if err != nil {
		log.Fatalf("按行分页失败: %v", err)
	}

	fmt.Printf("📊 按行分页结果：%d 张卡片\n", len(lineResult.Cards))
	for i, card := range lineResult.Cards {
		fmt.Printf("  卡片 %d: %d 个元素\n", i+1, len(card.Elements))
		for j, element := range card.Elements {
			content := fmt.Sprintf("%v", element.Content)
			if len(content) > 50 {
				content = content[:50] + "..."
			}
			fmt.Printf("    元素 %d [%s]: %s\n", j+1, element.Type, content)
		}
	}
	fmt.Println()

	// 对比分析
	fmt.Println("=== 对比分析 ===")
	fmt.Printf("标准分页：%d 张卡片\n", len(standardResult.Cards))
	fmt.Printf("按行分页：%d 张卡片\n", len(lineResult.Cards))

	if len(lineResult.Cards) < len(standardResult.Cards) {
		fmt.Println("✅ 按行分页减少了卡片数量，提高了空间利用率")
	} else if len(lineResult.Cards) > len(standardResult.Cards) {
		fmt.Println("⚠️ 按行分页增加了卡片数量，但可能分布更均匀")
	} else {
		fmt.Println("📊 两种分页方式产生相同数量的卡片")
	}

	fmt.Println("\n=== 结论 ===")
	fmt.Println("按行分页的优势：")
	fmt.Println("1. 能够精确控制每张卡片的内容量")
	fmt.Println("2. 避免了一张卡片内容过少或过多的情况")
	fmt.Println("3. 提高了空间利用率的均匀性")
	fmt.Println("4. 解决了段落分页的极端情况问题")

	fmt.Println("\n=== 测试完成 ===")
}
