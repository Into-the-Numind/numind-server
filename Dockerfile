# 多阶段构建 - 构建阶段
FROM golang:1.24.2-alpine AS builder

# 设置工作目录
WORKDIR /app

# 安装必要的系统依赖
RUN apk add --no-cache git ca-certificates tzdata

# 复制 go mod 文件
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 构建应用
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w -X main.Version=$(git describe --tags --always --dirty)" \
    -o /app/bin/numind ./cmd/numind

# 运行阶段
FROM alpine:3.19

# 安装必要的运行时依赖
RUN apk add --no-cache ca-certificates tzdata curl

# 创建非 root 用户
RUN addgroup -g 1001 -S numind && \
    adduser -u 1001 -S numind -G numind

# 设置工作目录
WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /app/bin/numind /app/numind

# 复制配置文件（如果有的话）
COPY config_dev.yaml /app/config_dev.yaml

# 设置正确的权限
RUN chown -R numind:numind /app && \
    chmod +x /app/numind

# 切换到非 root 用户
USER numind

# 暴露端口
EXPOSE 9091

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:9091/healthz || exit 1

# 设置环境变量
ENV GIN_MODE=release
ENV PORT=9091

# 启动应用
ENTRYPOINT ["/app/numind"]
