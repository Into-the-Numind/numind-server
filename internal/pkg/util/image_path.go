package util

import (
	"context"
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
	imagePath := GetImagePath()
	return filepath.Join(imagePath, "card", fmt.Sprintf("%d", cardID), filename)
}

// GetBookImageURL 获取书籍图片URL
func GetBookImageURL(bookID uint, filename string) string {
	imagePath := GetImagePath()
	return filepath.Join(imagePath, "book", fmt.Sprintf("%d", bookID), filename)
}

// 旧的 extractBasePath 已移除。

// GetDisplayURL 返回用于对外展示的图片URL（去掉/opt前缀）
func GetDisplayURL(path string) string {
    if path == "" {
        return ""
    }
    if strings.HasPrefix(path, "/opt/") {
        return strings.TrimPrefix(path, "/opt")
    }
    return path
}

// GetAvatarDisplayURL 获取头像展示URL（去掉/opt前缀）
func GetAvatarDisplayURL(avatarPath string) string {
    return GetDisplayURL(avatarPath)
}

// GetCardImageWithCOS 获取卡片图片URL，优先返回COS链接
// 如果COS启用且文件存在能生成签名URL，返回COS链接；否则返回本地路径
func GetCardImageWithCOS(ctx context.Context, cardID uint, localPath string) string {
    // 如果本地路径为空，直接返回空
    if localPath == "" {
        return ""
    }
    
    // 检查COS是否启用
    if !IsCOSEnabled() {
        // COS未启用，返回本地路径
        return GetDisplayURL(localPath)
    }
    
    // 构建COS对象键
    objectKey := fmt.Sprintf("card/%d/card_%d.webp", cardID, cardID)
    
    // 先检查文件是否存在于COS
    if !CheckObjectExists(ctx, objectKey) {
        // 文件在COS中不存在，返回本地路径
        return GetDisplayURL(localPath)
    }
    
    // 文件存在，尝试生成COS签名URL（10分钟有效期）
    signedURL, err := GenerateSignedURL(ctx, objectKey, 600)
    if err == nil && signedURL != "" {
        // 成功获取COS链接，返回COS URL
        return signedURL
    }
    
    // COS获取失败，返回本地路径
    return GetDisplayURL(localPath)
}

// GetBookImageWithCOS 获取书籍图片URL，优先返回COS链接
// 如果COS启用且文件存在能生成签名URL，返回COS链接；否则返回本地路径
func GetBookImageWithCOS(ctx context.Context, bookID uint, localPath string) string {
    // 如果本地路径为空，直接返回空
    if localPath == "" {
        return ""
    }
    
    // 检查COS是否启用
    if !IsCOSEnabled() {
        // COS未启用，返回本地路径
        return GetDisplayURL(localPath)
    }
    
    // 构建COS对象键
    objectKey := fmt.Sprintf("book/%d/book_%d.webp", bookID, bookID)
    
    // 先检查文件是否存在于COS
    if !CheckObjectExists(ctx, objectKey) {
        // 文件在COS中不存在，返回本地路径
        return GetDisplayURL(localPath)
    }
    
    // 文件存在，尝试生成COS签名URL（10分钟有效期）
    signedURL, err := GenerateSignedURL(ctx, objectKey, 600)
    if err == nil && signedURL != "" {
        // 成功获取COS链接，返回COS URL
        return signedURL
    }
    
    // COS获取失败，返回本地路径
    return GetDisplayURL(localPath)
}
