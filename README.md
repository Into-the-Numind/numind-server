# Numind Server  

## 技术栈

- Gin (Web框架)
- Gorm (ORM)
- MySQL (数据库)
- JWT (认证)
- BeautifulSoup (HTML解析)

## 快速开始

### 本地开发

```bash
# 克隆项目
git clone https://github.com/Into-the-Numind/numind-server.git
cd numind-server

# 安装依赖
go mod download

# 运行项目
go run cmd/numind/main.go -c config_dev.yaml
```

### Docker 部署

```bash
# 构建镜像
docker build -t numind-server .

# 运行容器
docker run -d --name numind-server -p 9091:9091 numind-server -c /app/config_dev.yaml
```

## CI/CD 部署

项目使用 GitHub Actions 进行自动化部署，支持多环境部署和滚动更新。

### 分支策略

- `develop` 分支: 自动部署到开发环境
- `release` 分支: 自动部署到QA环境  
- `product` 分支: 自动部署到生产环境

### 部署特性

- ✅ 滚动更新 (零停机部署)
- ✅ 健康检查验证
- ✅ 自动回滚机制
- ✅ 多环境支持
- ✅ 详细日志记录

详细部署文档请参考: [部署指南](docs/deployment.md)

## API文档

### Apifox  

