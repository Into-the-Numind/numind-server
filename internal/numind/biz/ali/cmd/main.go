package main

import (
	"fmt"
	"numind-server/internal/numind/biz/ali"
)

func main() {
	biz := ali.NewAliBiz(nil)

	// 千问文字模型流式调用示例
	messages := []map[string]string{
		{"role": "user", "content": "请用一句话介绍阿里云百炼平台"},
	}
	text, err := biz.QianwenTextStream(messages, 256, 0.5)
	if err != nil {
		fmt.Println("千问流式调用失败:", err)
	} else {
		fmt.Println("千问流式生成内容:", text)
	}

	// 万象图像模型流式调用示例
	// imgUrl, err := biz.WanxiangImageStream("一只可爱的猫咪，卡通风格", "cartoon", "1024*1024")
	// if err != nil {
	// 	fmt.Println("万象流式调用失败:", err)
	// } else {
	// 	fmt.Println("万象流式生成图片URL:", imgUrl)
	// }

	// 万象图像模型异步调用示例
	imgUrlAsync, err := biz.WanxiangImageAsync("一间有着精致窗户的花店，漂亮的木质门，摆放着花朵", "", "1024*1024")
	if err != nil {
		fmt.Println("万象异步调用失败:", err)
	} else {
		fmt.Println("万象异步生成图片URL:", imgUrlAsync)
	}
}
