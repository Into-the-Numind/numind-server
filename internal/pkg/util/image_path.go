package util

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/viper"
)

// GetImagePath 获取配置的图片路径
func GetImagePath() string {
	imagePath := viper.GetString("resource.image_path")
	if imagePath == "" {
		imagePath = "./images/upload" // 默认路径
	}
	return imagePath
}

// GetCardImagePath 获取卡片图片保存路径
func GetCardImagePath(cardID uint) string {
	imagePath := GetImagePath()
	return filepath.Join(imagePath, "card", fmt.Sprintf("%d", cardID))
}

// GetBookImagePath 获取书籍图片保存路径
func GetBookImagePath(bookID uint) string {
	imagePath := GetImagePath()
	return filepath.Join(imagePath, "book", fmt.Sprintf("%d", bookID))
}

// GetCardImageURL 获取卡片图片URL
func GetCardImageURL(cardID uint, filename string) string {
	imagePath := GetImagePath()
	return filepath.Join(imagePath, "card", fmt.Sprintf("%d", cardID), filename)
}

// GetBookImageURL 获取书籍图片URL
func GetBookImageURL(bookID uint, filename string) string {
	imagePath := GetImagePath()
	return filepath.Join(imagePath, "book", fmt.Sprintf("%d", bookID), filename)
}

// 旧的 extractBasePath 已移除。
