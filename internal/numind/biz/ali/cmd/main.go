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

	// stable-diffusion图像模型异步调用示例
	fmt.Println("\n开始测试stable-diffusion-3.5-large-turbo模型...")
	stableDiffusionUrl, err := biz.StableDiffusionImageAsync("未来城市中，人类与人工智能共同处理数据流，象征联机思考与独立能力的结合。半透明的机械臂与人类双手协作筛选发光的信息粒子，背景是流动的二进制代码瀑布，高科技办公室环境采用冷色调蓝银配色，超现实主义风格", "1024*1024")
	if err != nil {
		fmt.Println("stable-diffusion异步调用失败:", err)
	} else {
		fmt.Println("stable-diffusion异步生成图片URL:", stableDiffusionUrl)
	}
}
