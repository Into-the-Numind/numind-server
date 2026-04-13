# numind-server — Go 后端约束文件

---

## 1. 技术栈声明

Go 1.24 | Gin | GORM | MySQL 8.0 | Redis | JWT | Viper | Zap

---

## 2. 架构分层规则

三层架构，**单向依赖**：controller → biz → store，不可反向调用。

| 层 | 职责 | 禁止事项 |
|----|------|---------|
| **controller** (`internal/numind/controller/v1/`) | 参数校验 + 响应格式化 | 禁止写业务逻辑 |
| **biz** (`internal/numind/biz/`) | 业务逻辑层，核心代码写这里 | 禁止直接操作 HTTP 请求/响应 |
| **store** (`internal/numind/store/`) | 数据访问层，数据库操作 | 禁止包含业务判断 |

> **关键规则：不要在 controller 层写业务逻辑，业务逻辑统一放 biz 层。**

---

## 3. 编码规范

- 错误处理：使用 `fmt.Errorf("xxx: %w", err)` 链式包装，保留错误链
- 导出函数必须带注释，以函数名开头（Go doc 规范）
- 新增 API 端点必须在 `internal/numind/router.go` 中注册
- 涉及数据库 schema 变更必须创建 migration SQL 文件，放在 `migrations/` 目录
- **不要修改 `config_prod.yaml`**
- **不要在代码中硬编码 API 密钥、数据库密码等敏感信息**
- 修改 Go 代码后必须运行 `task lint` 通过后再提交

---

## 4. 开发命令

```bash
task dev            # 开发模式运行
task lint           # 代码检查（go vet + golangci-lint）
task fmt            # 代码格式化
task build          # 构建（含依赖安装 + 格式化）
go test ./...       # 轻量测试
task test           # 完整测试（含 race detection + coverage）
task deps           # 安装/整理 Go 依赖
```

---

## 5. 项目结构速查

```
cmd/numind/                     # 主服务入口
cmd/numind-admin/               # 管理后台入口
internal/numind/biz/            # 业务逻辑层
internal/numind/controller/v1/  # API 控制器
internal/numind/store/          # 数据访问层
internal/numind/router.go       # 路由配置（用户端）
internal/numind/admin_router.go # 路由配置（管理端）
internal/pkg/                   # 内部公共包
├── core/                       #   响应处理
├── errno/                      #   错误码定义
├── middleware/                  #   中间件（JWT、CORS 等）
├── model/                      #   数据模型
├── log/                        #   日志（Zap）
├── redis/                      #   Redis 工具
└── util/                       #   通用工具函数
internal/service/               # 外部服务调用（AI、向量数据库等）
migrations/                     # 数据库迁移 SQL
config_*.yaml                   # 环境配置（local/dev/qa/prod）
```
