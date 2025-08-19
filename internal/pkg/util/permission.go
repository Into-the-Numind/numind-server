package util

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsureDirectoryPermissions 确保目录存在且有正确的权限
func EnsureDirectoryPermissions(dirPath string) error {
	// 创建目录（如果不存在）
	if err := os.MkdirAll(dirPath, 0777); err != nil {
		return fmt.Errorf("failed to create directory %s: %v", dirPath, err)
	}

	// 设置目录权限
	if err := os.Chmod(dirPath, 0777); err != nil {
		return fmt.Errorf("failed to set permissions for %s: %v", dirPath, err)
	}

	// 验证目录权限
	if info, err := os.Stat(dirPath); err != nil {
		return fmt.Errorf("failed to stat directory %s: %v", dirPath, err)
	} else {
		fmt.Printf("🔧 权限修复：目录 %s 权限=%s\n", dirPath, info.Mode())
	}

	return nil
}

// EnsureCardDirectory 确保卡片目录存在且有正确权限
func EnsureCardDirectory(cardID uint) error {
	cardDir := GetCardImagePath(cardID)
	return EnsureDirectoryPermissions(cardDir)
}

// EnsureBookDirectory 确保书籍目录存在且有正确权限
func EnsureBookDirectory(bookID uint) error {
	bookDir := GetBookImagePath(bookID)
	return EnsureDirectoryPermissions(bookDir)
}

// EnsureParentDirectory 确保父目录存在且有正确权限
func EnsureParentDirectory(filePath string) error {
	parentDir := filepath.Dir(filePath)
	return EnsureDirectoryPermissions(parentDir)
}
