# 容器时区配置

## 概述

本文档说明如何在Docker容器中设置时区为东八区（Asia/Shanghai）。

## 配置方法

### 1. Dockerfile 配置

在Dockerfile中添加以下配置：

```dockerfile
# 安装tzdata包
RUN apk add --no-cache tzdata

# 设置时区环境变量
ENV TZ=Asia/Shanghai

# 创建时区链接和配置文件
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone
```

### 2. Docker Compose 配置

在docker-compose.yml中添加环境变量：

```yaml
environment:
  - TZ=Asia/Shanghai
```

### 3. 运行时配置

也可以在运行容器时通过命令行设置：

```bash
docker run -e TZ=Asia/Shanghai your-image
```

## 验证时区设置

### 方法1: 进入容器检查

```bash
# 进入运行中的容器
docker exec -it container_name sh

# 检查时区
date
echo $TZ
ls -la /etc/localtime
cat /etc/timezone
```

### 方法2: 使用测试脚本

```bash
# 运行测试脚本
docker exec container_name /app/scripts/test-timezone.sh
```

## 时区信息

- **时区名称**: Asia/Shanghai
- **UTC偏移**: UTC+8
- **标准时间**: 中国标准时间 (CST)
- **夏令时**: 无

## 注意事项

1. **Alpine Linux**: 使用`tzdata`包提供时区数据
2. **权限**: 确保容器有权限修改`/etc/localtime`和`/etc/timezone`
3. **重启**: 时区设置可能需要重启容器才能完全生效
4. **应用层**: Go程序会自动使用系统时区，无需额外配置

## 故障排除

### 时区不正确

1. 检查`TZ`环境变量是否正确设置
2. 确认`tzdata`包已安装
3. 验证时区文件链接是否正确

### 权限问题

如果遇到权限错误，确保容器以root用户运行或使用`--privileged`标志。

## 相关文件

- `Dockerfile` - 容器构建配置
- `docker-compose.yml` - 容器编排配置
- `scripts/test-timezone.sh` - 时区测试脚本
