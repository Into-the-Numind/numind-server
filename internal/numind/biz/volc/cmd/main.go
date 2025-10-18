package main

import (
	"context"
	"fmt"
	"numind-server/internal/numind/biz/volc"
)

func main() {
	biz := volc.NewVolcBiz(nil)
	ctx := context.Background()

	// 火山引擎文字模型流式调用示例
	messages := []map[string]string{
		{"role": "user", "content": "请用一句话介绍火山引擎"},
	}
	text, err := biz.VolcTextStream(ctx, messages, 256, 0.5)
	if err != nil {
		fmt.Println("火山引擎流式调用失败:", err)
	} else {
		fmt.Println("火山引擎流式生成内容:", text)
	}

	// 通用内容生成示例
	content, err := biz.GenerateArticleContent(
		"这是一段测试文本，用于验证火山引擎的内容生成功能。",
		"summary",
		100,
		nil,
		"",
	)
	if err != nil {
		fmt.Println("通用内容生成失败:", err)
	} else {
		fmt.Println("通用内容生成结果:", content)
	}
}
