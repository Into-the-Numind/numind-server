# 飞书个人工作空间：运行时 Keyring 配置

飞书个人工作空间启用时，CLI HOME 快照和待执行操作使用独立的、可轮换的 AES-256 Keyring 加密。密钥绝不能写入 `config_*.yaml`、镜像、Git 或数据库；部署机只从 `/opt/numind/<env>/secrets.env` 注入它。

## 何时读取

服务启动时，`internal/numind/helper.go` 已调用 Viper 的环境变量绑定：配置键中的 `.` 会变成 `_`，并统一加上 `NUMIND_` 前缀。`NewBiz` 在 `features.feishu_integration.enabled=true` 时调用 `buildConfiguredFeishuService`，它从同一个 Viper 实例读取以下值。没有通过完整校验时，飞书工具不会注册到该进程。

| Viper 配置键 | 环境变量 | 是否敏感 | 作用 |
| --- | --- | --- | --- |
| `security.thirdparty_token_key` | `NUMIND_SECURITY_THIRDPARTY_TOKEN_KEY` | 是 | 既有 receipt/cursor HMAC 根密钥；与 Keyring 分离 |
| `feishu.keyring` | `NUMIND_FEISHU_KEYRING` | 是 | 唯一的 Keyring 密钥入口 |
| `feishu.key_version` | `NUMIND_FEISHU_KEY_VERSION` | 否 | 当前写入使用的版本 |
| `feishu.runtime_base` | `NUMIND_FEISHU_RUNTIME_BASE` | 否 | 解密后临时 HOME 的可写根目录；不可和旧 `home_base` 混用 |
| `feishu.auth_owner` | `NUMIND_FEISHU_AUTH_OWNER` | 否 | 授权会话租约 token 的实例前缀 |
| `features.feishu_integration.enabled` | `NUMIND_FEATURES_FEISHU_INTEGRATION_ENABLED` | 否 | 仅在所有前置项就绪后设为 `true` |

`security.thirdparty_token_key` 仍是既有 receipt/cursor HMAC 根密钥，不能拿它替代 Keyring 的某一项。

## Keyring 的唯一格式

`NUMIND_FEISHU_KEYRING` 必须是**单行、无外层 shell 引号的严格 JSON 数组**；数组项只允许恰好两个小写字段 `version` 和 `key`。`version` 必须是 canonical 小写 `[a-z0-9._-]`（最长 32 字符）；`key` 必须是 canonical Base64 编码的 32 字节 AES-256 密钥。

```dotenv
# /opt/numind/dev/secrets.env (权限 0600；示意，不是可用密钥)
NUMIND_SECURITY_THIRDPARTY_TOKEN_KEY=REPLACE_WITH_EXISTING_RECEIPT_HMAC_ROOT
NUMIND_FEISHU_KEYRING=[{"version":"v1","key":"REPLACE_WITH_CANONICAL_BASE64_AES_256_KEY"}]
NUMIND_FEISHU_KEY_VERSION=v1
NUMIND_FEISHU_RUNTIME_BASE=/opt/numind/dev/feishu-runtime
NUMIND_FEISHU_AUTH_OWNER=numind-dev-feishu
NUMIND_FEATURES_FEISHU_INTEGRATION_ENABLED=true
```

不要使用 YAML map、JSON object、逗号分隔字符串、Base64 之外的编码，或给整段 JSON 加 shell 引号。以下都会令该进程 fail closed：未知字段、JSON 末尾额外内容、重复/大写版本、重复密钥材料、不是 32 字节的 Key、当前版本不在列表中、或历史 vault/operation 仍引用已移除的版本。

密钥轮换时，先保留旧项并追加新项、把 `NUMIND_FEISHU_KEY_VERSION` 切到新版本，验证所有历史数据可读取后，才可在后续维护窗口清除旧项。当前版本仅用于新写入；历史版本只用于解密。

## 启用前检查

1. 确保 `runtime_base` 是服务用户可写的临时目录，且不保存长期明文 HOME。
2. 用 0600 权限维护 `secrets.env`；部署脚本以 Docker `--env-file` 注入，文件不会进入镜像。
3. 在保留旧 Keyring 条目的前提下重启服务。启动日志不得输出 Keyring 内容。
4. 确认 feature 已成功装配后，再启用用户侧连接入口；不要把 flag 当成忽略密钥校验的开关。
