package main

import (
	"fmt"
	"numind-server/internal/numind/biz/volc"
)

func main() {
	biz := volc.NewVolcBiz(nil)

	content := "与功能价值的客观性不同，情绪价值是主观的。用户购买情绪价值，不是为有形之物付费，而是为无形之物付费，是为生理唤起、认知标记、彼此心领神会的共识，以及这些掺杂在一起后营造的氛围付费。如果说创造功能价值要有工具思维，那么创造情绪价值"

	// 摘要生成
	summary, err := biz.GenerateArticleContent(content, "summary", 100, nil, "")
	if err != nil {
		fmt.Println("摘要生成失败:", err)
	} else {
		fmt.Println("摘要生成结果:", summary)
	}

	// 标注生成
	annotation, err := biz.GenerateArticleContent(content, "", 100, nil, "")
	if err != nil {
		fmt.Println("标注生成失败:", err)
	} else {
		fmt.Println("标注生成结果:", annotation)
	}
}
