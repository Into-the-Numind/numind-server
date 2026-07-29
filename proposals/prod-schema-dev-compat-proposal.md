# Dev 数据库历史结构兼容 — 提案

## §1 方案概述

不修改 Dev 现有业务记录，而是让上线检查识别两类安全状态：

1. Prod 从零创建的最终结构；
2. Dev 已经稳定运行、且经逐字段确认的历史兼容结构。

飞书证明表缺少的两个外键不靠重建表解决。升级脚本会先确认 3 条现有记录都能找到
对应父记录、父子字段类型一致，再幂等补外键。智能体约束增加当前代码会产生的空状态
以及一个精确历史清理标记，并分别绑定“运行中”和“已删除”条件，不放开任意字符串。
同时停止 `agent_attachment` 启动时 AutoMigrate，避免服务启动绕过升级包修改字段。

## §2 工作量与周期

- 预估工作量：当天完成修正、双审、隔离 MySQL 8 演练和 Dev 真库演练。
- 报价：内部上线工程，不单独报价。
- 交付时间线：修正通过后立即返回原 Dev → Prod 上线主线。

## §3 技术可行性

### 现有功能复用

- 复用现有 `00-preflight.sql` / migration / `02-verify.sql` 三段式升级包。
- 复用 MySQL `information_schema` 做字段、索引、外键和孤儿记录检查。
- 复用构建机 MySQL 8.4 隔离 runner 和 Dev 已验证可恢复备份。

### 方案选择

- 拒绝清洗 Dev 行：会改变智能体历史和附件缓存，不符合数据保护目标。
- 拒绝跳过 Dev：会失去真实历史结构下的演练价值。
- 选择兼容门禁：只放行精确已知结构，并补齐可安全新增的外键。

### 技术风险

- 放宽过度：通过完整状态矩阵、完整元数据指纹和负向测试防止。
- 外键添加失败：preflight 先检查孤儿和父子字段类型，失败即停止。
- 约束过宽：只增加空字符串和精确
  `zombie_cleanup_2026_05_28`，并绑定状态/删除条件，保留完整 CHECK 指纹校验。
- 字段顺序误判：表 contract 不再把 `ORDINAL_POSITION` 作为功能契约，
  但仍校验全部列名、类型、可空性、默认值、extra、列字符集/collation、
  索引方向/可见性/表达式、引擎和表 collation。
- 启动时隐形改表：移除 `AgentAttachment` 的启动 AutoMigrate；完整 Dev 服务启动前后
  比较附件新旧字段与 proof 行投影。

### 涉及仓库

- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性

N/A，不新增 LLM 调用。

## §4 产品需求定义

### 用户故事

- 作为长期使用 Dev 验证新功能的产品负责人，我需要上线包兼容真实 Dev 历史结构，
  以便它能在不改历史数据的情况下被完整演练后再用于 Prod。

### 验收标准

- [ ] Dev preflight 无 FAIL。
- [ ] migration 双次执行和 verify 均通过。
- [ ] Dev 全部保护投影、checksum 和核心表行数前后不变。
- [ ] 字段顺序改变不影响表 contract。
- [ ] 附件仅接受最终结构或已确认的 Dev 早期结构。
- [ ] Prod 全缺字段、migration 可产生的 final 前缀、完整 final、完整 legacy
  组成唯一允许状态矩阵；其余 mixed/第三形态失败。
- [ ] 缺失飞书证明外键可从 0 个补为精确 2 个；孤儿或类型不兼容必须失败。
- [ ] Agent CHECK 接受 running+空状态和 deleted+精确旧清理标记，拒绝
  terminated+空、active+zombie、任意其他值及 `extXresume:`。
- [ ] migration 自身在任何 mutation 前重做附件、Agent、proof 外键/孤儿/类型 gate。
- [ ] 服务完整启动不会 ALTER 附件表，附件 5 个解析字段和 3 条 proof 记录的逐行
  哈希前后相同。
- [ ] Prod SELECT-only preflight 仍通过。

### 边界情况

- 同名但错误外键、部分外键、错误删除规则：失败。
- 已存在孤儿 proof 行：失败。
- 附件出现第三种字段组合：失败。
- legacy 附件行的 SHA/大小/页数 NULL 扫描必须通过真实 MySQL + GORM 读取测试；
  若不能读取则不得放行该 legacy 结构。
- 表多列、少列、错误类型或错误索引：失败。
- migration 重跑：不得重复外键或配置行。

### 权限规则与 UI

无用户权限或 UI 变化。
