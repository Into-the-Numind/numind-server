package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

func main() {
	fmt.Println("=== 无头浏览器渲染器调试测试 ===")

	// 创建测试配置
	config := &pagination.PaginationConfig{
		Card: pagination.CardConfig{
			Width:  800,
			Height: 600,
			Padding: struct {
				Top    int `json:"top"`
				Right  int `json:"right"`
				Bottom int `json:"bottom"`
				Left   int `json:"left"`
			}{
				Top:    40,
				Right:  40,
				Bottom: 40,
				Left:   40,
			},
		},
	}

	// 创建简化版渲染器
	renderer := card.NewSimpleHeadlessRenderer(config)

	// 创建测试卡片数据 - 使用简单的测试数据
	testElements := []pagination.Element{
		{
			Type:    pagination.ElementTypeTitle,
			Content: "测试标题",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "这是一个测试内容，用于验证无头浏览器渲染器是否正常工作。",
		},
	}

	// 将元素转换为JSON
	processedText, err := json.Marshal(testElements)
	if err != nil {
		log.Fatalf("Failed to marshal elements: %v", err)
	}

	fmt.Printf("生成的JSON数据: %s\n", string(processedText))

	// 创建测试卡片
	testCard := &model.CardM{
		Model:         gorm.Model{ID: 999},
		ProcessedText: string(processedText),
		SortOrder:     1,
	}

	fmt.Println("开始渲染卡片...")

	// 渲染卡片
	renderedCard, err := renderer.RenderCardToImage(testCard)
	if err != nil {
		log.Fatalf("Failed to render card: %v", err)
	}

	fmt.Printf("✅ 卡片渲染成功!\n")
	fmt.Printf("卡片ID: %d\n", renderedCard.CardID)
	fmt.Printf("图片URL: %s\n", renderedCard.ImageURL)
	fmt.Printf("图片尺寸: %dx%d\n", renderedCard.Width, renderedCard.Height)
	fmt.Printf("排序: %d\n", renderedCard.SortOrder)

	// 检查生成的图片文件
	imagePath := fmt.Sprintf("./images/upload/card/%d/card_%d.webp", renderedCard.CardID, renderedCard.CardID)
	if _, err := os.Stat(imagePath); err == nil {
		fmt.Printf("✅ 图片文件存在: %s\n", imagePath)
		// 获取文件大小
		if info, err := os.Stat(imagePath); err == nil {
			fmt.Printf("   文件大小: %d bytes\n", info.Size())
		}
	} else {
		fmt.Printf("❌ 图片文件不存在: %s\n", imagePath)
	}

	// 检查调试HTML文件
	if files, err := os.ReadDir("."); err == nil {
		for _, file := range files {
			if len(file.Name()) >= 11 && file.Name()[:11] == "debug_simple_" && file.Name()[len(file.Name())-5:] == ".html" {
				fmt.Printf("✅ 调试HTML文件: %s\n", file.Name())
				if content, err := os.ReadFile(file.Name()); err == nil {
					fmt.Printf("HTML内容长度: %d bytes\n", len(content))
					fmt.Printf("HTML内容前500字符:\n%s\n", string(content[:min(500, len(content))]))
				}
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
