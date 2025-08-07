package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"numind-server/internal/numind/biz/card"
	"numind-server/internal/numind/biz/pagination"
	"numind-server/internal/pkg/model"

	"github.com/spf13/viper"
	"gorm.io/gorm"
)

func main() {
	// 加载配置
	viper.SetConfigName("config_local")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	
	if err := viper.ReadInConfig(); err != nil {
		log.Fatalf("Failed to read config: %v", err)
	}
	
	// 创建测试数据
	testData := []map[string]interface{}{
		{
			"type":    "title",
			"content": "未来竞争力:独立思考者的进化之路",
		},
		{
			"type":    "image",
			"content": "/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/res/upload/book/30/book_30_1754538342.jpg",
		},
	}

	// 转换为JSON
	processedText, err := json.Marshal(testData)
	if err != nil {
		log.Fatalf("Failed to marshal test data: %v", err)
	}

	// 创建测试卡片
	testCard := &model.CardM{
		Model:         gorm.Model{ID: 999999},
		ProcessedText: string(processedText),
		SortOrder:     0,
	}

	// 创建封面渲染器
	config := pagination.GetDefaultConfig()
	renderer := card.NewCoverRenderer(config)

	// 渲染封面
	renderedCard, err := renderer.RenderCoverCardToImage(testCard)
	if err != nil {
		log.Fatalf("Failed to render cover card: %v", err)
	}

	fmt.Printf("封面渲染成功！\n")
	fmt.Printf("卡片ID: %d\n", renderedCard.CardID)
	fmt.Printf("图片URL: %s\n", renderedCard.ImageURL)
	fmt.Printf("尺寸: %dx%d (4:3比例)\n", renderedCard.Width, renderedCard.Height)
	fmt.Printf("排序: %d\n", renderedCard.SortOrder)

	// 检查生成的图片文件是否存在
	actualPath := fmt.Sprintf("images/upload/card/%d/card_%d.png", renderedCard.CardID, renderedCard.CardID)
	if _, err := os.Stat(actualPath); os.IsNotExist(err) {
		fmt.Printf("警告: 生成的图片文件不存在: %s\n", actualPath)
	} else {
		fmt.Printf("图片文件已生成: %s\n", actualPath)
		fmt.Printf("文件大小: %d bytes\n", func() int64 {
			if info, err := os.Stat(actualPath); err == nil {
				return info.Size()
			}
			return 0
		}())
	}
}
