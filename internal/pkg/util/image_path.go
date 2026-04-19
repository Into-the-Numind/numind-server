package util

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

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

// GetAvatarWithCOS 获取用户头像URL，优先返回COS链接
// 如果COS启用且文件存在能生成签名URL，返回COS链接；否则返回本地路径
func GetAvatarWithCOS(ctx context.Context, userID uint, localPath string) string {
	if localPath == "" {
		return ""
	}

	if !IsCOSEnabled() {
		return GetDisplayURL(localPath)
	}

	fileName := filepath.Base(localPath)
	if fileName == "" {
		return GetDisplayURL(localPath)
	}

	objectKey := fmt.Sprintf("avatars/%d/%s", userID, fileName)

	if !CheckObjectExists(ctx, objectKey) {
		return GetDisplayURL(localPath)
	}

	signedURL, err := GenerateSignedURL(ctx, objectKey, 600)
	if err == nil && signedURL != "" {
		return signedURL
	}

	return GetDisplayURL(localPath)
}
