# 图片路径配置示例

## 概述

本文档展示了不同环境下图片保存路径的配置示例，包括卡片渲染图片和书籍图片的保存路径。

## 配置结构

### 1. 基础配置
```yaml
resource:
  image_path: "{base_path}/image/upload"
```

### 2. 路径结构
```
{image_path}/card/{card_id}/card_{card_id}.png
{image_path}/book/{book_id}/book_{book_id}.png
```

## 环境配置示例

### 1. 生产环境 (Production)
```yaml
# config_prod.yaml
resource:
  image_path: "/opt/numind/image/upload"
```

**实际路径示例:**
- 卡片图片: `/opt/numind/image/upload/card/18/card_18.png`
- 书籍图片: `/opt/numind/image/upload/book/11/book_11.png`

**URL示例:**
- 卡片URL: `/opt/numind/card/18/card_18.png`
- 书籍URL: `/opt/numind/book/11/book_11.png`

### 2. QA环境
```yaml
# config_qa.yaml
resource:
  image_path: "/opt/numind/image/upload"
```

**实际路径示例:**
- 卡片图片: `/opt/numind/image/upload/card/18/card_18.png`
- 书籍图片: `/opt/numind/image/upload/book/11/book_11.png`

### 3. 开发环境 (Development)
```yaml
# config_dev.yaml
resource:
  image_path: "/opt/numind/image/upload"
```

**实际路径示例:**
- 卡片图片: `/opt/numind/image/upload/card/18/card_18.png`
- 书籍图片: `/opt/numind/image/upload/book/11/book_11.png`

### 4. 本地环境 (Local)
```yaml
# config_local.yaml
resource:
  image_path: /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/res/upload
```

**实际路径示例:**
- 卡片图片: `/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/res/upload/card/18/card_18.png`
- 书籍图片: `/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/res/upload/book/11/book_11.png`

**URL示例:**
- 卡片URL: `/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/card/18/card_18.png`
- 书籍URL: `/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/book/11/book_11.png`

## 工具函数使用

### 1. 获取图片路径
```go
import "numind-server/internal/pkg/util"

// 获取配置的图片路径
imagePath := util.GetImagePath()

// 获取卡片图片保存路径
cardPath := util.GetCardImagePath(cardID)

// 获取书籍图片保存路径
bookPath := util.GetBookImagePath(bookID)
```

### 2. 构建图片URL
```go
// 获取卡片图片URL
cardURL := util.GetCardImageURL(cardID, filename)

// 获取书籍图片URL
bookURL := util.GetBookImageURL(bookID, filename)
```

## 目录结构示例

### 生产环境目录结构
```
/opt/numind/
├── image/
│   └── upload/
│       ├── card/
│       │   ├── 18/
│       │   │   └── card_18.png
│       │   └── 19/
│       │       └── card_19.png
│       └── book/
│           ├── 11/
│           │   └── book_11.png
│           └── 12/
│               └── book_12.png
```

### 本地环境目录结构
```
/Users/neozhang/go/src/github.com/Into-the-Numind/numind-server/
├── res/
│   └── upload/
│       ├── card/
│       │   ├── 18/
│       │   │   └── card_18.png
│       │   └── 19/
│       │       └── card_19.png
│       └── book/
│           ├── 11/
│           │   └── book_11.png
│           └── 12/
│               └── book_12.png
```

## 部署注意事项

### 1. 目录权限
确保配置的图片路径有正确的读写权限：
```bash
# 创建目录
mkdir -p /opt/numind/image/upload/card
mkdir -p /opt/numind/image/upload/book

# 设置权限
chmod 755 /opt/numind/image/upload
chmod 755 /opt/numind/image/upload/card
chmod 755 /opt/numind/image/upload/book
```

### 2. 磁盘空间
监控图片存储目录的磁盘使用情况：
```bash
# 检查磁盘使用
df -h /opt/numind/image/upload

# 查看图片文件大小
du -sh /opt/numind/image/upload/card/*
du -sh /opt/numind/image/upload/book/*
```

### 3. 清理策略
定期清理旧的图片文件：
```bash
# 清理30天前的图片文件
find /opt/numind/image/upload/card -name "*.png" -mtime +30 -delete
find /opt/numind/image/upload/book -name "*.png" -mtime +30 -delete
```

## 测试验证

### 1. 路径测试
```bash
# 检查配置的图片路径
grep "image_path:" config_*.yaml

# 检查目录是否存在
ls -la /opt/numind/image/upload/card/
ls -la /opt/numind/image/upload/book/
```

### 2. 权限测试
```bash
# 测试写入权限
touch /opt/numind/image/upload/card/test.png
rm /opt/numind/image/upload/card/test.png
```

### 3. 功能测试
```bash
# 运行卡片渲染测试
./scripts/test-card-rendering.sh
```

## 故障排除

### 1. 目录不存在
**错误:** `failed to create card directory`
**解决:** 检查配置的`image_path`是否正确，确保有创建目录的权限

### 2. 权限不足
**错误:** `permission denied`
**解决:** 检查目录权限，确保应用有读写权限

### 3. 磁盘空间不足
**错误:** `no space left on device`
**解决:** 清理旧文件或扩展磁盘空间

### 4. 路径配置错误
**错误:** `invalid path`
**解决:** 检查配置文件中的`image_path`格式是否正确 