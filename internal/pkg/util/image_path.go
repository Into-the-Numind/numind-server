package util

import (
	"fmt"
	"path/filepath"
	"strings"

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
	basePath := extractBasePath(GetImagePath())
	return fmt.Sprintf("%s/card/%d/%s", basePath, cardID, filename)
}

// GetBookImageURL 获取书籍图片URL
func GetBookImageURL(bookID uint, filename string) string {
	basePath := extractBasePath(GetImagePath())
	return fmt.Sprintf("%s/book/%d/%s", basePath, bookID, filename)
}

// extractBasePath 从image_path中提取基础路径
func extractBasePath(imagePath string) string {
	// 移除末尾的/upload或/upload/
	path := strings.TrimSuffix(imagePath, "/upload")
	path = strings.TrimSuffix(path, "/upload/")

	// 如果路径以/opt/numind开头，返回/opt/numind
	if strings.HasPrefix(path, "/opt/numind") {
		return "/opt/numind"
	}

	// 否则返回image_path的父目录
	return filepath.Dir(path)
}
