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