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

	// 示例数据
	elements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "为什么高价值的信息几乎从不流向普通人？",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "因为流不动，容易被误解，甚至被\"拒收\"。价值越高的东西，越考验人的理解能力。",
		},
		{
			Type: pagination.ElementTypeList,
			Content: []string{
				"《道德经》中的\"无为\"被解读成\"什么都不做\"。",
				"\"以德报怨\"，被误解为\"被人欺负要用爱心感化\"。",
			},
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
}
