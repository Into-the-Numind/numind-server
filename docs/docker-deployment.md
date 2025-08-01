# Docker 部署指南

## 概述

本指南介绍如何使用 Docker 部署 Numind 服务器，包括微信支付证书的配置。

## 前置要求

1. **Docker** 已安装
2. **微信支付证书** 已获取并放置到正确位置
3. **SSL 证书** 已放置在服务器上

## 证书配置

### 1. 获取微信支付证书

1. 登录微信商户平台: https://pay.weixin.qq.com
2. 进入 "账户中心" -> "API安全"
3. 下载 API 证书和私钥文件

### 2. 放置证书文件

将证书文件放置到以下位置：

```
/opt/numind/config/cert/
├── apiclient_key.pem    # 商户私钥文件
└── wechatpay_cert.pem   # 微信支付证书
```

### 3. 设置证书权限

```bash
# 运行服务器端证书设置脚本
./scripts/setup-server-certificates.sh

# 或手动设置权限
chmod 600 /opt/numind/config/cert/apiclient_key.pem
chmod 644 /opt/numind/config/cert/wechatpay_cert.pem
```

### 4. SSL 证书配置

确保服务器上的 SSL 证书目录存在：

```bash
# 检查 SSL 证书目录
ls -la /etc/ssl/certimate/youshu.asia/

# 确保证书文件存在
ls -la /etc/ssl/certimate/youshu.asia/cert.crt
ls -la /etc/ssl/certimate/youshu.asia/cert.key
```

## 构建 Docker 镜像

```bash
# 构建镜像
docker build -t numind-server:latest .

# 查看构建的镜像
docker images | grep numind-server
```

## 运行容器

### 开发环境

```bash
# 运行容器
docker run -d \
  --name numind-server \
  -p 9091:9091 \
  -p 9092:9092 \
  -v /opt/numind:/opt/numind:ro \
  -v /etc/ssl/certimate/youshu.asia:/etc/ssl/certimate/youshu.asia:ro \
  numind-server:latest \
  -c /app/config_dev.yaml
```

### 生产环境

```bash
# 运行容器
docker run -d \
  --name numind-server \
  -p 9091:9091 \
  -p 9092:9092 \
  -v /opt/numind:/opt/numind:ro \
  -v /etc/ssl/certimate/youshu.asia:/etc/ssl/certimate/youshu.asia:ro \
  -e GIN_MODE=release \
  numind-server:latest \
  -c /app/config_prod.yaml
```

## 容器内文件结构

```
/app/
├── numind                    # 可执行文件
├── config_dev.yaml          # 开发环境配置
└── config_prod.yaml         # 生产环境配置

/opt/numind/                 # 映射的主机目录
├── config/
│   └── cert/               # 证书目录
│       ├── apiclient_key.pem
│       └── wechatpay_cert.pem
└── logs/                    # 日志目录

/etc/ssl/certimate/youshu.asia/  # SSL 证书目录
├── cert.crt                 # SSL 证书文件
└── cert.key                 # SSL 私钥文件
```

## 健康检查

容器包含健康检查，可以通过以下命令查看状态：

```bash
# 查看容器状态
docker ps

# 查看健康检查日志
docker inspect numind-server | grep -A 10 Health

# 手动健康检查
curl http://localhost:9091/healthz
```

## 日志查看

```bash
# 查看容器日志
docker logs numind-server

# 实时查看日志
docker logs -f numind-server

# 查看最近的日志
docker logs --tail 100 numind-server
```

## 故障排除

### 1. 证书文件问题

```bash
# 检查证书文件是否存在
docker exec numind-server ls -la /opt/numind/config/cert/

# 检查证书文件权限
docker exec numind-server ls -la /opt/numind/config/cert/*.pem
```

### 2. 配置文件问题

```bash
# 检查配置文件
docker exec numind-server cat /app/config_dev.yaml

# 检查配置语法
docker exec numind-server /app/numind -c /app/config_dev.yaml --help
```

### 3. 网络连接问题

```bash
# 检查端口是否开放
netstat -tlnp | grep 9091
netstat -tlnp | grep 9092

# 测试 API 接口
curl http://localhost:9091/healthz
curl https://localhost:9092/healthz
```

## 安全注意事项

1. **证书文件安全**
   - 确保证书文件权限正确 (600/644)
   - 不要将证书文件提交到版本控制
   - 生产环境建议使用密钥管理服务

2. **容器安全**
   - 使用非 root 用户运行
   - 限制容器资源使用
   - 定期更新基础镜像

3. **网络安全**
   - 使用 HTTPS 进行外部通信
   - 配置防火墙规则
   - 监控网络流量

## 性能优化

1. **资源限制**
```bash
docker run -d \
  --name numind-server \
  --memory=512m \
  --cpus=1.0 \
  -p 9091:9091 \
  numind-server:latest
```

2. **日志轮转**
```bash
docker run -d \
  --name numind-server \
  --log-driver json-file \
  --log-opt max-size=10m \
  --log-opt max-file=3 \
  -p 9091:9091 \
  numind-server:latest
```

## 监控和维护

1. **资源监控**
```bash
# 查看容器资源使用
docker stats numind-server

# 查看容器详细信息
docker inspect numind-server
```

2. **日志监控**
```bash
# 监控错误日志
docker logs -f numind-server | grep ERROR

# 监控支付相关日志
docker logs -f numind-server | grep -i pay
```

3. **定期维护**
   - 定期更新证书文件
   - 清理旧日志文件
   - 监控磁盘空间使用 