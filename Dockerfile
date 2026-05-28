# 构建阶段 - 在容器内编译源码
FROM golang:1.24-bookworm AS builder

# 安装必要的构建工具
RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    libmupdf-dev \
    libsqlite3-dev \
    git

# =====================================================
# Docker 分层缓存优化说明
# =====================================================
# 第 1 层：依赖层（go.mod/sum 不变时缓存命中）
# - go mod download 结果会被缓存
# - 只有修改 go.mod 时才会重新下载
#
# 第 2 层：构建层（每次代码变更都会重建）
# - 源码变更触发重新编译
# - 依赖未变时使用缓存的依赖层
#
# 手动刷新缓存：修改此行时间戳 -> # Cache Bust: 2026-02-26-001
# =====================================================

WORKDIR /app

# 第 1 层：复制依赖文件并下载（缓存层）
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

# 第 2 层：复制源码并编译（非缓存层，每次重新构建）
COPY . .
# CGO_ENABLED=1 是必须的，因为使用了 go-fitz (libmupdf) 和 sqlite-vec
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o numind ./cmd/numind

# 运行阶段
FROM ubuntu:22.04

# 设置环境变量避免交互式安装
ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Shanghai

# 安装系统依赖
# 注意: libmupdf-dev 不需要 — go-fitz 使用自带的预编译静态库，MuPDF 已嵌入 Go 二进制
# 注意: python3-pip 仅在下方 pip install 阶段需要，安装完成后移除以节省空间
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    tzdata \
    python3 \
    python3-pip \
    antiword \
    libgomp1 \
    && rm -rf /var/lib/apt/lists/*

# (agent-mode-sandbox-integration #4) docker CLI for the DooD pattern.
# WITH_DOCKER_CLI=true is passed by the dev build script; prod default
# (WITH_DOCKER_CLI=false) does NOT install docker-cli, keeping the
# attack surface minimal. Even with the CLI installed, the runtime
# behavior depends on sandbox.backend in config — disabled by default.
ARG WITH_DOCKER_CLI=false
RUN if [ "$WITH_DOCKER_CLI" = "true" ]; then \
        apt-get update && \
        DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends docker.io && \
        rm -rf /var/lib/apt/lists/*; \
    fi

# 安装 Python 依赖 - 第一层：基础核心库 (变化频率低，体积大)
# 强制使用 CPU 版本以减小镜像体积
RUN pip3 install --no-cache-dir --upgrade pip && \
    pip3 install --no-cache-dir torch --index-url https://download.pytorch.org/whl/cpu

# 安装 Python 依赖 - 第二层：功能库
# 使用清华大学 PyPI 镜像（中国境内构建提速 200x+，全球可访问，无副作用）
RUN pip3 install --no-cache-dir \
    --index-url https://pypi.tuna.tsinghua.edu.cn/simple \
    --trusted-host pypi.tuna.tsinghua.edu.cn \
    pymupdf \
    python-docx \
    sentence-transformers \
    numpy \
    fastapi \
    uvicorn \
    python-multipart \
    "markitdown[all]" && \
    pip3 uninstall -y pip setuptools && \
    rm -rf /usr/lib/python3/dist-packages/pip /usr/lib/python3/dist-packages/setuptools

# 第三层：预下载语义切分模型 (实现 99.9% 可用性)
# 将模型固化在镜像中，避免由于网络问题导致的生产环境失效
ENV SENTENCE_TRANSFORMERS_HOME=/app/model_cache
ENV HF_HOME=/app/model_cache
ENV HF_ENDPOINT=https://hf-mirror.com

# 创建用户
RUN groupadd -g 1001 numind && useradd -m -u 1001 -g numind -G audio,video numind

# 创建缓存目录并赋予权限
RUN mkdir -p /app/model_cache && chown -R numind:numind /app/model_cache

# 复制下载脚本 (用于运行时自动修复/首次初始化)
COPY scripts/download_models.py /app/scripts/download_models.py
RUN chmod +x /app/scripts/download_models.py

# 设置时区
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone

# 设置工作目录
WORKDIR /app

# 定义构建参数，默认为dev环境
ARG ENV=dev

# 只复制对应环境的配置文件（避免 prod 配置泄露到 dev 镜像）
COPY config_${ENV}.yaml ./

# 复制 configs/ 目录（含 tool-display.yaml 等运行时配置）。
# 必须在 runtime stage 显式 COPY：builder stage 的 `COPY . .` 只让构建期能读，
# binary 运行时按 cwd 加载 `configs/tool-display.yaml`。
# 历史教训（2026-05-22）：narration provider 加载这个 yaml，缺失则整个 narration
# 子系统在 biz.Init 阶段 silently disabled（agent.WithNarrationProvider(nil)），
# 学员侧看不到「正在调用 web_search」之类工具调用进度。
COPY configs /app/configs

# 复制 skills/ 目录（pptx-author / docx-author / xlsx-author / pdf-from-html）。
# skills.NewRegistry 在启动时读 sandbox.skills_root，AcquireForSkill 在运行时
# WalkDir 该目录再 CopyToContainer 到 sandbox 子容器。skill 是代码工件，
# 与版本绑定，COPY 进镜像；config_*.yaml 的 skills_root 改为 /app/skills。
# 历史教训（2026-05-28）：之前 skills_root 默认指向 host 路径 /opt/numind/skills，
# 但部署机 bind-mount 是 /opt/numind/${ENV}:/opt/numind/${ENV}，容器内 /opt/numind/skills
# 不存在，导致 invoke_skill 静默失败，Agent 调 pptx-author 时 PPT 生成报错。
COPY skills /app/skills

# 从构建阶段复制编译好的二进制文件
COPY --from=builder /app/numind /app/numind
COPY scripts /app/scripts
# Copy jieba dictionary files
COPY --from=builder /go/pkg/mod/github.com/yanyiwu/gojieba@v1.4.6/deps/cppjieba/dict /app/dict
RUN chown -R numind:numind /app/dict

# 预创建图片输出目录，避免运行期权限问题
RUN chmod 755 /opt && \
    mkdir -p /opt/numind/dev/image/upload && \
    mkdir -p /opt/numind/prod/image/upload && \
    mkdir -p /opt/numind/qa/image/upload && \
    mkdir -p /opt/numind/image/upload && \
    mkdir -p /app/logs && \
    mkdir -p /app/temp

# 设置所有权和权限 - 与CI/CD配置保持一致
RUN chown -R numind:numind /opt/numind && \
    chown -R numind:numind /app && \
    chmod -R 775 /opt/numind && \
    chmod -R 775 /app/logs && \
    chmod -R 775 /app/temp && \
    chmod +x /app/numind

# 验证二进制文件存在且可执行
RUN ls -la /app/numind && \
    echo "✅ 运行阶段二进制文件验证成功"

# 复制启动脚本（包含语义切分模型检查）- 必须在 USER 切换之前
COPY scripts/docker-entrypoint.sh /app/start.sh
RUN chmod +x /app/start.sh && chown numind:numind /app/start.sh

# 切换到非 root 用户
USER numind

# 暴露端口
EXPOSE 9091 9092

# 设置环境变量
ENV GIN_MODE=release
ENV PORT=9091
ENV TZ=Asia/Shanghai

# 性能优化
ENV GOGC=50
ENV GOMEMLIMIT=16GiB

# 健康检查
HEALTHCHECK --interval=30s --timeout=10s --start-period=300s --retries=3 \
    CMD curl -f http://localhost:9091/healthz || exit 1

# 启动应用
ENTRYPOINT ["/app/start.sh"]
CMD []
