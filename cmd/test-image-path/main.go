package main

import (
	"fmt"
	"os"
	"path/filepath"

	"numind-server/internal/pkg/util"

	"github.com/spf13/viper"
)

func main() {
	fmt.Println("🧪 开始测试图片路径配置...")

	// 加载配置文件
	viper.SetConfigName("config_local")
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")

	if err := viper.ReadInConfig(); err != nil {
		fmt.Printf("❌ 配置文件加载失败: %v\n", err)
		return
	}
	fmt.Println("✅ 配置文件加载成功")

	// 检查配置值
	imagePath := viper.GetString("resource.image_path")
	fmt.Printf("📁 配置的image_path: %s\n", imagePath)

	// 测试GetImagePath函数
	utilImagePath := util.GetImagePath()
	fmt.Printf("🔧 util.GetImagePath(): %s\n", utilImagePath)

	// 测试卡片路径
	cardID := uint(233)
	cardPath := util.GetCardImagePath(cardID)
	fmt.Printf("🃏 卡片 %d 的保存路径: %s\n", cardID, cardPath)

	// 测试书籍路径
	bookID := uint(119)
	bookPath := util.GetBookImagePath(bookID)
	fmt.Printf("📚 书籍 %d 的保存路径: %s\n", bookID, bookPath)

	// 检查目录是否存在
	if _, err := os.Stat(imagePath); err != nil {
		fmt.Printf("❌ 配置的image_path目录不存在: %v\n", err)
	} else {
		fmt.Printf("✅ 配置的image_path目录存在\n")
	}

	// 检查card子目录
	cardBasePath := filepath.Join(imagePath, "card")
	if _, err := os.Stat(cardBasePath); err != nil {
		fmt.Printf("❌ card子目录不存在: %v\n", err)
	} else {
		fmt.Printf("✅ card子目录存在: %s\n", cardBasePath)
	}

	// 检查book子目录
	bookBasePath := filepath.Join(imagePath, "book")
	if _, err := os.Stat(bookBasePath); err != nil {
		fmt.Printf("❌ book子目录不存在: %v\n", err)
	} else {
		fmt.Printf("✅ book子目录存在: %s\n", bookBasePath)
	}

	// 检查具体的卡片目录
	if _, err := os.Stat(cardPath); err != nil {
		fmt.Printf("❌ 卡片 %d 目录不存在: %v\n", cardID, err)
	} else {
		fmt.Printf("✅ 卡片 %d 目录存在: %s\n", cardID, cardPath)
	}

	// 检查具体的书籍目录
	if _, err := os.Stat(bookPath); err != nil {
		fmt.Printf("❌ 书籍 %d 目录不存在: %v\n", bookID, err)
	} else {
		fmt.Printf("✅ 书籍 %d 目录存在: %s\n", bookID, bookPath)
	}

	fmt.Println("\n🎉 图片路径配置测试完成！")
}
