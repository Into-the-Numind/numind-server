package main

import (
	"encoding/json"
	"fmt"
	"log"

	"numind-server/internal/numind/biz/pagination"
)

func main() {
	// 创建分页引擎
	engine := pagination.NewPaginationEngine(pagination.GetDefaultConfig())

	// 使用你提供的示例数据
	elements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "联机时代的独立思考者：未来竞争力进化论",
		},
		{
			Type:    pagination.ElementTypeSubtitle,
			Content: "未来职业竞争力的关键要素",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "这个时代需要每个人都成为'联机的独立思考者'，融合全球智慧与个人洞察力。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "在人工智能盛行、行业无边界的时代，最具竞争力的人能够：用机器学习处理信息，用大脑整合创新思想，用系统思维解决复杂问题。",
		},
		{
			Type: pagination.ElementTypeList,
			Content: []string{
				"我今天做的事，机器能做吗？",
				"我今天做的事，会被外包吗？",
				"我今天做的事，明天会做得更好吗？",
			},
		},
		{
			Type:    pagination.ElementTypeSubtitle,
			Content: "认知方式的革命性转变",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "读100本书并试图记住，就像非要背下整本电话簿才开始拨号。未来核心认知能力应包含：信息搜索能力、深度思考能力、趋势洞察能力。",
		},
		{
			Type:    pagination.ElementTypeQuote,
			Content: "人类'记住知识'的方式持续了两千多年，而近20年内新认知方式突然成为主流——这种变化是不连续的、跳跃式的。",
		},
	}

	// 执行分页
	result, err := engine.Paginate(elements)
	if err != nil {
		log.Fatalf("分页失败: %v", err)
	}

	// 输出结果
	fmt.Printf("分页结果：共 %d 个卡片\n", len(result.Cards))
	for i, card := range result.Cards {
		fmt.Printf("\n=== 卡片 %d ===\n", i+1)
		for j, element := range card.Elements {
			fmt.Printf("%d. [%s] %v\n", j+1, element.Type, element.Content)
		}
	}

	// 将结果转换为JSON并打印
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Fatalf("JSON序列化失败: %v", err)
	}
	fmt.Printf("\n=== JSON 输出 ===\n%s\n", string(jsonData))

	// 打印配置信息
	config := pagination.GetDefaultConfig()
	fmt.Printf("\n=== 配置信息 ===\n")
	fmt.Printf("卡片尺寸: %dx%d\n", config.Card.Width, config.Card.Height)
	fmt.Printf("内边距: 上%d 右%d 下%d 左%d\n",
		config.Card.Padding.Top,
		config.Card.Padding.Right,
		config.Card.Padding.Bottom,
		config.Card.Padding.Left)

	fmt.Printf("\n样式配置:\n")
	for elementType, style := range config.Styles {
		fmt.Printf("  %s: 字体%dpx, 行高%dpx, 颜色%s\n",
			elementType, style.FontSize, style.LineHeight, style.Color)
	}
}
