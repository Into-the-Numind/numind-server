# 书籍图片下载功能实现

## 功能概述

当通过阿里万象API生成书籍封面图片时，系统会自动将远程图片下载并保存到本地路径 `resource.image_path/upload/book/{id}`，并将数据库中的 `ImageUrl` 字段更新为本地文件路径。

## 实现细节

### 1. 图片下载函数

在 `internal/numind/controller/v1/book/create.go` 中新增了 `downloadAndSaveImage` 函数：

```go
func downloadAndSaveImage(imageURL string, bookID uint) (string, error) {
    // 获取本地保存路径
    localPath := util.GetBookImagePath(bookID)
    
    // 确保目录存在
    if err := os.MkdirAll(localPath, 0755); err != nil {
        return "", fmt.Errorf("failed to create directory: %w", err)
    }
    
    // 生成文件名
    filename := fmt.Sprintf("book_%d_%d.jpg", bookID, time.Now().Unix())
    localFilePath := filepath.Join(localPath, filename)
    
    // 下载图片
    resp, err := http.Get(imageURL)
    if err != nil {
        return "", fmt.Errorf("failed to download image: %w", err)
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return "", fmt.Errorf("failed to download image, status: %d", resp.StatusCode)
    }
    
    // 创建本地文件
    file, err := os.Create(localFilePath)
    if err != nil {
        return "", fmt.Errorf("failed to create local file: %w", err)
    }
    defer file.Close()
    
    // 复制内容到本地文件
    _, err = io.Copy(file, resp.Body)
    if err != nil {
        return "", fmt.Errorf("failed to save image: %w", err)
    }
    
    // 返回本地文件路径
    return localFilePath, nil
}
```

### 2. 路径工具函数

使用 `internal/pkg/util/image_path.go` 中的 `GetBookImagePath` 函数：

```go
func GetBookImagePath(bookID uint) string {
    imagePath := GetImagePath()
    return filepath.Join(imagePath, "book", fmt.Sprintf("%d", bookID))
}
```

### 3. 创建书籍流程

修改后的创建书籍流程：

1. **调用千问API**：处理用户输入的文本，生成结构化内容和图片描述
2. **提取标题**：从结构化内容中提取书籍标题
3. **生成图片**：如果有图片描述，调用万象API生成图片
4. **创建书籍记录**：先创建书籍记录以获取ID
5. **下载图片**：将远程图片下载到本地路径
6. **更新记录**：将本地图片路径保存到数据库
7. **处理卡片**：继续处理分页和卡片创建

### 4. 文件路径结构

```
resource.image_path/
└── upload/
    └── book/
        └── {book_id}/
            └── book_{book_id}_{timestamp}.jpg
```

## 配置要求

确保在配置文件中设置了正确的图片路径：

```yaml
resource:
  image_path: "./images/upload"  # 或者 "/opt/numind/images/upload"
```

## 错误处理

- 图片下载失败不会影响书籍创建流程
- 所有错误都会记录到日志中
- 如果图片保存失败，书籍记录仍然会创建，只是 `ImageUrl` 字段为空

## 测试方法

使用提供的测试脚本：

```bash
./scripts/test-book-image-download.sh
```

## 注意事项

1. **文件权限**：确保应用有权限创建目录和文件
2. **磁盘空间**：确保有足够的磁盘空间存储图片
3. **网络连接**：需要能够访问阿里云万象API
4. **文件清理**：建议定期清理旧的图片文件

## 数据库字段

`BookM` 模型中的 `ImageUrl` 字段会保存本地文件路径，例如：
- `/opt/numind/images/upload/book/123/book_123_1640995200.jpg`
- `./images/upload/book/123/book_123_1640995200.jpg`
