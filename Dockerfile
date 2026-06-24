# numind-server 业务镜像
# ML_BASE_TAG: 运行阶段 FROM 的 numind-ml-base 镜像 tag。
# 必须在「第一个 FROM」之前声明 —— Docker 规则：只有首个 FROM 之前的 global ARG
# 才能在后续 FROM 行展开。若声明在 builder stage 内会被 scope 到该 stage，runtime
# FROM 取不到 → 展开成空 → "invalid reference format" 构建失败。
# 依赖变更重建 base 后 bump 这里；或 docker build --build-arg ML_BASE_TAG=<tag> 覆盖。
ARG ML_BASE_TAG=20260603

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
# -tags sqlite_fts5：启用 mattn/go-sqlite3 的 FTS5 全文检索扩展（混合检索 BM25 关键词通道）。
# 无此 tag 时 fts_chunks 虚拟表建表会失败，store 自动降级为纯向量检索（见 SQLiteVecStore.initFTS）。
RUN CGO_ENABLED=1 GOOS=linux go build -tags sqlite_fts5 -ldflags="-s -w" -o numind ./cmd/numind

# 运行阶段 — FROM 预构建 ML base 镜像
# 系统依赖（ca-certificates/curl/tzdata/python3/antiword/libgomp1）+ torch CPU +
# sentence-transformers/pymupdf/markitdown 等已固化在 base，业务构建不再每次部署
# 跨境重下（详见 Dockerfile.ml-base + scripts/cicd/build-ml-base.sh）。
# ML_BASE_TAG 在本文件顶部以 global ARG 声明（须在首个 FROM 之前才能在此展开）；
# 依赖变更时：重建 base（build-ml-base.sh）→ bump 顶部 ML_BASE_TAG 默认值。
FROM ccr.ccs.tencentyun.com/youshunumind/numind-ml-base:${ML_BASE_TAG}

# 设置环境变量避免交互式安装（base 已设，此处冗余保留以兼容下方可选的 docker CLI apt 块）
ENV DEBIAN_FRONTEND=noninteractive
ENV TZ=Asia/Shanghai

# ffmpeg：视频/音频转写抽音频必需（xhs 视频逐字稿 + monitor/会议）。ML base 不带，运行阶段装。
RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ffmpeg && \
    rm -rf /var/lib/apt/lists/*

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

# Python ML 依赖（torch CPU + sentence-transformers/pymupdf/python-docx/numpy/
# fastapi/uvicorn/python-multipart/markitdown[all]）已在 Dockerfile.ml-base 中
# 固化进 base 镜像，此处不再 pip install——避免每次部署从 pytorch.org / PyPI 重下。
# 校验 base 含这些依赖：scripts/cicd/verify-ml-base.sh

# =====================================================
# lark-cli (feishu-agent-connect / feishu-integration)
# =====================================================
# 把飞书官方 lark-cli (@larksuite/cli, MIT) 的 linux/amd64 standalone 二进制装到
# PATH (/usr/local/bin/lark-cli)。用途：feishu provisioner 在用户「连接飞书」时
# os/exec `lark-cli config init --new`，用标准 device-code 流帮用户建飞书自建应用、
# 取 appId/appSecret（见 internal/numind/biz/feishu/provisioner_cli.go）。日常写
# 文档/发消息走 oapi-sdk-go（DB 里的 token），不依赖 lark-cli。
#
# 为何下 standalone 二进制而非 npm i -g @larksuite/cli：
#   - npm 包的 bin 是个 Node launcher (scripts/run.js)，真正的 lark-cli 是它在
#     postinstall (scripts/install.js) 下载的自包含 Go 二进制。直接取那个二进制
#     就免装 Node + npm，运行镜像更小、攻击面更少。
#   - 实测 (2026-06-24)：tar 内 `lark-cli` = `ELF 64-bit ... x86-64, statically
#     linked, Go ... stripped`，纯自包含，运行时无任何外部依赖。
#
# 版本固定 v1.0.56（与 spike-bootstrap 实跑验证 device-code 流 + Go 解析逻辑所依据
# 的版本一致）。升级版本时：同步 bump LARK_CLI_VERSION + LARK_CLI_SHA256
# （sha256 取自 npm 包内 checksums.txt 的 lark-cli-<ver>-linux-amd64.tar.gz 行）。
#
# 下载源顺序：npmmirror 国内镜像优先（构建机在成都骨干网，GitHub 跨境慢/不稳），
# GitHub release 兜底。两源同一 artifact，SHA256 校验是完整性主控（镜像≠官方时
# 直接 fail 构建）；下载主机仅这两个，避免被改成任意 URL。
# 最后一步构建期自检 `lark-cli --version`：版本命令跑通=二进制可执行且架构匹配；
# 关 update/skills notifier 避免 --version 探测去拉网络更新检查导致挂起/失败。
ARG LARK_CLI_VERSION=1.0.56
ARG LARK_CLI_SHA256=93c1254889ebf0a3a562869515af15188075a95bbe9a15e5711d9c9a4af4d8c2
RUN set -eux; \
    arch="$(uname -m)"; \
    if [ "$arch" != "x86_64" ] && [ "$arch" != "amd64" ]; then \
        echo "ERROR: lark-cli install only supports linux/amd64, got ${arch}" >&2; exit 1; \
    fi; \
    archive="lark-cli-${LARK_CLI_VERSION}-linux-amd64.tar.gz"; \
    tmp="$(mktemp -d)"; \
    mirror_url="https://registry.npmmirror.com/-/binary/lark-cli/v${LARK_CLI_VERSION}/${archive}"; \
    github_url="https://github.com/larksuite/cli/releases/download/v${LARK_CLI_VERSION}/${archive}"; \
    ( curl -fSL --connect-timeout 15 --max-time 180 -o "${tmp}/${archive}" "${mirror_url}" \
      || curl -fSL --connect-timeout 15 --max-time 180 -o "${tmp}/${archive}" "${github_url}" ); \
    echo "${LARK_CLI_SHA256}  ${tmp}/${archive}" | sha256sum -c -; \
    tar -xzf "${tmp}/${archive}" -C "${tmp}" lark-cli; \
    install -m 0755 "${tmp}/lark-cli" /usr/local/bin/lark-cli; \
    rm -rf "${tmp}"; \
    LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1 LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1 lark-cli --version

# 把上面的自检 env 固化成运行期 ENV：否则容器内每次 lark-cli 调用（config show /
# apps +init / config init --new）都可能触发 update-check / skills-notifier 的网络探测，
# 在无外网或外网慢的环境下挂起，拖垮 PollCredentials。provisioner_cli.go 的 env()
# 也会再注入一份，确保继承环境和显式构造环境两边都干净。
ENV LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1
ENV LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1

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
