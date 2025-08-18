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

# 安装必要的运行时依赖，包括Chrome for headless rendering和WebP工具
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    curl \
    chromium \
    nss \
    freetype \
    freetype-dev \
    harfbuzz \
    ca-certificates \
    ttf-freefont \
    fontconfig \
    font-noto-cjk \
    libwebp-tools \
    && rm -rf /var/cache/apk/*

# 设置时区为东八区
ENV TZ=Asia/Shanghai
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone

# 验证WebP工具安装
RUN cwebp -version && echo "✅ WebP工具安装成功"

# 创建非 root 用户
RUN addgroup -g 1001 -S numind && \
    adduser -u 1001 -S numind -G numind

# 设置工作目录
WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /app/bin/numind /app/numind

# 定义构建参数，默认为dev环境
ARG ENV=dev

# 根据环境复制对应的配置文件
COPY config_${ENV}.yaml /app/config_${ENV}.yaml

# 预创建图片输出目录，避免运行期权限问题
# 确保/opt目录权限正确，然后创建子目录
RUN chmod 755 /opt && \
    mkdir -p /opt/numind/dev/image/upload && \
    mkdir -p /opt/numind/prod/image/upload && \
    mkdir -p /opt/numind/qa/image/upload && \
    mkdir -p /opt/numind/image/upload && \
    chown -R numind:numind /opt/numind && \
    chmod -R 777 /opt/numind

# 设置应用目录权限
RUN chown -R numind:numind /app && \
    chmod +x /app/numind

# 切换到非 root 用户
USER numind

# 暴露端口
EXPOSE 9091 9092



# 设置环境变量
ENV GIN_MODE=release
ENV PORT=9091
# Chrome headless环境变量
ENV CHROME_BIN=/usr/bin/chromium-browser
ENV CHROME_PATH=/usr/bin/chromium-browser
ENV CHROMIUM_FLAGS="--headless --no-sandbox --disable-dev-shm-usage --disable-gpu --disable-web-security --disable-features=VizDisplayCompositor"

# 启动应用
ENTRYPOINT ["/app/numind"]
