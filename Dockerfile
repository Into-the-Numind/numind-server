# 外部构建阶段 - 用于 CI/CD 中使用预构建的二进制文件
FROM scratch AS external-binary
ARG BINARY_PATH
COPY $BINARY_PATH /app/numind

# 运行阶段 - 基于Ubuntu以获得更好的Chrome支持
FROM ubuntu:22.04

# 设置环境变量避免交互式安装
ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Shanghai

# 安装系统依赖
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    wget \
    gnupg \
    bash \
    file \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

# 安装Chrome依赖和字体
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    fonts-liberation \
    fonts-noto-cjk \
    fonts-wqy-microhei \
    fonts-wqy-zenhei \
    libasound2 \
    libatk-bridge2.0-0 \
    libatk1.0-0 \
    libatspi2.0-0 \
    libcups2 \
    libdbus-1-3 \
    libdrm2 \
    libgbm1 \
    libgtk-3-0 \
    libnspr4 \
    libnss3 \
    libx11-6 \
    libxcb1 \
    libxcomposite1 \
    libxdamage1 \
    libxext6 \
    libxfixes3 \
    libxkbcommon0 \
    libxrandr2 \
    xdg-utils \
    && rm -rf /var/lib/apt/lists/*

# 安装Google Chrome
RUN wget -q -O - https://dl.google.com/linux/linux_signing_key.pub | gpg --dearmor -o /usr/share/keyrings/googlechrome-linux-keyring.gpg \
    && echo "deb [arch=amd64 signed-by=/usr/share/keyrings/googlechrome-linux-keyring.gpg] http://dl.google.com/linux/chrome/deb/ stable main" > /etc/apt/sources.list.d/google-chrome.list \
    && apt-get update \
    && apt-get install -y google-chrome-stable \
    && rm -rf /var/lib/apt/lists/*

# 设置时区
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone

# 验证时区设置
RUN date && echo "当前时区: $(cat /etc/timezone)" && echo "✅ 时区设置验证成功"

# 验证Chrome安装
RUN google-chrome --version && echo "Chrome installation successful"

# 创建非 root 用户 - 确保UID为1001，与CI/CD配置一致
RUN groupadd -g 1001 numind && useradd -u 1001 -g numind -G audio,video numind

# 创建Chrome数据目录并设置权限
RUN mkdir -p /home/numind/.config/google-chrome \
    && chown -R numind:numind /home/numind

# 设置工作目录
WORKDIR /app

# 定义构建参数，默认为dev环境
ARG ENV=dev

# 根据环境复制对应的配置文件
COPY config_${ENV}.yaml /app/config_${ENV}.yaml

# 根据构建参数选择二进制文件来源
ARG BINARY_SOURCE=external-binary
# 使用条件复制，根据 BINARY_SOURCE 参数选择来源
COPY --from=external-binary /app/numind /app/numind

# 验证配置文件复制成功
RUN ls -la /app/config_*.yaml && \
    echo "✅ 配置文件复制成功"

# 预创建图片输出目录，避免运行期权限问题
# 确保/opt目录权限正确，然后创建子目录
RUN chmod 755 /opt && \
    mkdir -p /opt/numind/dev/image/upload/card && \
    mkdir -p /opt/numind/dev/image/upload/book && \
    mkdir -p /opt/numind/prod/image/upload/card && \
    mkdir -p /opt/numind/prod/image/upload/book && \
    mkdir -p /opt/numind/qa/image/upload/card && \
    mkdir -p /opt/numind/qa/image/upload/book && \
    mkdir -p /opt/numind/image/upload/card && \
    mkdir -p /opt/numind/image/upload/book && \
    mkdir -p /app/logs && \
    mkdir -p /app/temp

# 设置所有权和权限 - 与CI/CD配置保持一致
RUN chown -R numind:numind /opt/numind && \
    chown -R numind:numind /app && \
    chmod -R 775 /opt/numind && \
    chmod -R 775 /app/logs && \
    chmod -R 775 /app/temp && \
    chmod +x /app/numind && \
    # 确保父目录也有正确权限
    chmod 775 /opt/numind/dev && \
    chmod 775 /opt/numind/dev/image && \
    chmod 775 /opt/numind/dev/image/upload && \
    chmod 775 /opt/numind/dev/image/upload/card && \
    chmod 775 /opt/numind/dev/image/upload/book

# 验证二进制文件存在且可执行
RUN ls -la /app/numind && \
    file /app/numind && \
    ldd /app/numind 2>/dev/null || echo "Static linked or no dependencies" && \
    echo "✅ 运行阶段二进制文件验证成功"

# 切换到非 root 用户
USER numind

# 暴露端口
EXPOSE 9091 9092

# 设置环境变量
ENV GIN_MODE=release
ENV PORT=9091
ENV TZ=Asia/Shanghai

# Chrome headless环境变量 - 针对分页渲染优化
ENV CHROME_BIN=/usr/bin/google-chrome
ENV CHROME_PATH=/usr/bin/google-chrome
ENV CHROMIUM_FLAGS="--headless --no-sandbox --disable-dev-shm-usage --disable-gpu --disable-web-security --disable-features=VizDisplayCompositor,Translate,BackForwardCache --disable-background-timer-throttling --disable-renderer-backgrounding --disable-backgrounding-occluded-windows --disable-ipc-flooding-protection --max_old_space_size=16384 --memory-pressure-off --disable-background-networking --disable-default-apps --disable-extensions --disable-sync --metrics-recording-only --no-first-run --disable-logging --disable-breakpad --disable-plugins --disable-component-extensions-with-background-pages --disable-client-side-phishing-detection --disable-hang-monitor --disable-prompt-on-repost --disable-domain-reliability --disable-field-trial-config --disable-background-mode --disable-software-rasterizer --disable-canvas-aa --disable-2d-canvas-clip-aa --disable-gl-drawing-for-tests --disable-features=AudioServiceOutOfProcess --disable-blink-features=AutomationControlled"

# 设置内存限制和性能优化
ENV NODE_OPTIONS="--max-old-space-size=16384"
ENV GOGC=50
ENV GOMEMLIMIT=16GiB

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
    CMD curl -f http://localhost:9091/healthz || exit 1

# 创建启动脚本
RUN echo '#!/bin/bash\n\
# 根据环境变量选择配置文件\n\
if [ -n "$APP_ENV" ]; then\n\
    CONFIG_FILE="/app/config_${APP_ENV}.yaml"\n\
else\n\
    CONFIG_FILE="/app/config_dev.yaml"\n\
fi\n\
\n\
# 检查配置文件是否存在\n\
if [ ! -f "$CONFIG_FILE" ]; then\n\
    echo "错误: 配置文件不存在: $CONFIG_FILE"\n\
    echo "可用的配置文件:"\n\
    ls -la /app/config_*.yaml\n\
    exit 1\n\
fi\n\
\n\
echo "使用配置文件: $CONFIG_FILE"\n\
exec /app/numind -c "$CONFIG_FILE" "$@"' > /app/start.sh && \
    chmod +x /app/start.sh

# 启动应用
ENTRYPOINT ["/app/start.sh"]
CMD []
