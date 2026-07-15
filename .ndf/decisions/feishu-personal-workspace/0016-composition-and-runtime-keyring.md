# ADR 0016: 完整 composition 与运行时密钥环配置

- Date: 2026-07-14
- Stage: S4 Task 12
- Status: accepted

## Context

飞书个人工作空间的受控 CLI、加密 HOME vault、授权恢复和 Agent 原 tool call 恢复必须作为一张完整依赖图发布。若只发布其中一部分，可能出现未清理的凭据、不可恢复的等待操作，或旧的宽权限路径重新暴露。

应用级 vault 密钥还需要支持轮换：新数据使用 current key，保留数据仍可按其冻结版本解密。此前 map 形式的 Viper 配置会把 `V1`/`v1` 归一化合并，无法安全发现冲突；而单纯 YAML 列表又不能由现有字符串环境变量安全注入，导致已开启的环境实际无法装配完整服务。

## Decision

1. `NewBiz` 只在 feature 开启、固定 1.0.68 runner 已验证、vault startup cleanup、catalog、confirmation、operation/auth recovery bridge、Task 11 shared resumer/reclaimer/supervisor 和 runtime keyring 都成功后，才发布一个完整的私有 workspace graph 并注册两个 Agent 工具。失败一律不发布半初始化服务；旧 Client/ConnectOrchestrator 不进入 production hot path。
2. keyring 是 version → cipher 的冻结运行时 map：current version 只用于 seal，已落库 vault/operation/result 只按其保存版本 open。启动扫描 retained vault、operation 与成功 result sealed blob；版本缺失、不规范、格式错误或未配置均 fail closed。
3. 配置只接受有序 `{version,key}` entry list。YAML map 一律拒绝；version 必须 canonical lowercase，版本和 key material 都不得重复。`NUMIND_FEISHU_KEYRING` 是唯一环境变量入口，且只接受严格 JSON array：拒绝 object/map、未知或重复字段、大小写变体、尾随 JSON、非法 Base64 和缺少 current version。错误不得回显 key material。
4. `feishu.key_version`、`runtime_base`、`auth_owner` 和 feature flag 保持可由同一 Viper 环境变量约定覆盖；local/dev 只保留非敏感默认值，真实 keyring 仅在部署 secret 环境中提供。

## Consequences

- 第一期能在不将密钥提交到仓库的前提下真正开启飞书能力；错误或歧义配置会失效关闭，而不是选择不确定的密钥。
- 密钥轮换需要在安全环境变量中同时保留仍被历史数据引用的条目，直到相应数据安全清理。
- 后续 Task 13 必须使用此已发布的 workspace graph 和同一个 dispatcher，不能重新构建任何 auth 或恢复通道。
