# 配置加载逻辑说明

## 配置加载策略

系统启动时**只加载代码中硬编码的配置项**到 `system_config` 表中，不再从 yaml 文件批量加载配置。

## 硬编码配置项

所有需要管理的配置项都硬编码在 `internal/numind/biz/config/config_definitions.go` 中：

1. `ai_prompts.text_processing` - AI文本处理提示词
2. `volc.model` - 火山引擎模型
3. `volc.temperature` - 火山引擎温度参数
4. `volc.tokens` - 火山引擎Token数量
5. `ali.text.model` - 阿里云文本模型

## 启动时配置同步流程

### 小程序服务端 (`internal/numind/helper.go`)

```go
// 1. 初始化Redis
redis.Init()

// 2. 同步硬编码配置到数据库
configBiz.InitDefaultConfigs(ctx)
// - 从 GetManagedConfigDefinitions() 获取硬编码配置定义
// - 对比数据库，添加缺失的配置项
// - 更新标题和描述（不覆盖用户修改的值）
// - 清理旧配置项

// 3. 启动配置变更监听器
configBiz.StartConfigChangeListener(ctx)
```

### 后台管理系统 (`internal/numind-admin/helper.go`)

```go
// 同样的流程
// 不再调用 initSystemConfigs()（已注释）
```

## 配置初始值来源

配置的初始值（DefaultValue）按以下优先级：

1. **yaml配置文件** - 如果配置文件中存在该键，使用yaml中的值
2. **硬编码fallback** - 如果yaml中不存在，使用代码中定义的fallback值

例如：
- `volc.model` 的初始值：优先从 `config_local.yaml` 的 `volc.model` 读取，如果没有则使用 `"doubao-seed-1-6-flash-250828"`

## 配置更新

- **用户修改的值会被保留** - 启动时同步不会覆盖用户已修改的配置值
- **标题和描述会自动更新** - 如果代码中的定义有变化，会自动更新数据库中的标题和描述

## Redis配置

在 `config_local.yaml` 中配置Redis（结构同MySQL）：

```yaml
redis:
  host: 127.0.0.1      # Redis 机器 IP
  port: 6379           # Redis 端口
  password: 123456     # Redis 密码
  db: 0                # Redis 数据库编号
  pool_size: 10        # Redis 连接池大小
  min_idle_conns: 5    # Redis 最小空闲连接数
```

## 验证配置加载

启动应用后，检查日志：

```
System configs synchronized successfully
Config synchronization completed total_managed=5 total_existing=5
```

检查数据库：

```sql
SELECT `key`, title, value, description FROM system_config;
```

应该只看到5个硬编码的配置项。

## 注意事项

1. **不再从yaml批量加载** - `initSystemConfigs()` 函数已不再调用
2. **只管理硬编码配置** - 只有代码中定义的5个配置项会被管理
3. **用户值受保护** - 启动时同步不会覆盖用户修改的配置值
4. **Redis必须配置** - 虽然Redis不可用时系统会降级，但建议配置Redis以获得最佳性能

