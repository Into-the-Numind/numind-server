# 板块 1: 多模态 Fallback Track

> 让 agent 在同一会话切换不同 modality 模型时，**用户上传的图片 / PDF 不再静默挂掉**。

## S0: Requirement Card

### 痛点

当前 agent mode 设计假设 LLM 是多模态的（仿 ClaudeCode 哲学）。但我们要接入 5 个新模型，其中 **3 个是单模态**：

| 模型 | 多模态？ |
|---|---|
| MiMo V2.5 Pro | ✅ |
| Kimi K2.5 / K2.6 | ✅ |
| GLM 5.1 | ❌ 文本 only |
| MiniMax M2.7 | ❌ 文本 only |
| Qwen 3.7 Max | ❌ 文本 only |

**当前若用户切到单模态模型后上传图片**：
1. 前端 chip 显示"上传成功"
2. 后端把 base64 ImageBlock 塞进 messages
3. 单模态 provider 返回 HTTP 400（unrecognized image format）或 silently 丢失图片
4. 整个 agent run terminal_reason=model_error

### 用户故事

> 张三是医疗器械销售，他在 agent mode 里：
> 1. 用 qwen-vl 多模态模型上传客户截图分析
> 2. 中途切到 GLM 5.1（性价比更高用来做长文本对话）
> 3. 又上传一张产品图
> 4. **期望**：agent 给他文字描述的总结，不挂掉
> 5. **当前**：agent run terminal_reason=model_error，前端报错

### 目标

- ✅ 单模态模型切换 + 图片上传 → 不挂掉
- ✅ 用户得到基于"图片文字描述"的回答（信息有损但不"瞎"）
- ✅ 多模态模型仍走原生 base64 inline（高质量）
- ✅ 用户感知最小：UI 只显示"为单模态模型生成图片描述..."状态

### 不在本板块范围

- ❌ 音频 / 视频处理（V2 再说）
- ❌ entity 三元组（销售关系图）
- ❌ vector embedding 检索

## 整体架构（4 层防护）

```
┌─────────────────────────────────────────────────────────┐
│                  用户上传图片 / PDF                       │
└────────────────────────┬────────────────────────────────┘
                         │
                         ▼
        ┌────────────────────────────────────┐
        │ Layer 1: 上传时双模态固化         │
        │ - 存 COS 原始 bytes                │
        │ - 异步跑 VLM 生 vision_description │
        │ - 异步跑 OCR 生 ocr_text           │
        │ - 拼成 text_fallback 字段          │
        │ - fallback_ready=true              │  ← task-02
        └────────────────────────────────────┘
                         │
                         ▼
        ┌────────────────────────────────────┐
        │ 用户在 agent mode 切换 model       │
        │ (qwen-vl → GLM 5.1)                │
        └────────────────────────┬───────────┘
                                 │
                                 ▼
        ┌────────────────────────────────────┐
        │ Layer 2: capability matrix 路由   │
        │ - 查 ai_service.capabilities_json │  ← task-01
        │ - 若 accepts_image=true → 路径 A   │
        │ - 若 accepts_image=false → 路径 B  │
        └─────────┬──────────────────┬───────┘
                  │                  │
        路径 A：原生inline       路径 B：文本 fallback
                  │                  │
                  ▼                  ▼
        base64 ImageBlock      TextBlock(text_fallback)
                  │                  │
                  └────────┬─────────┘
                           │
                           ▼
        ┌────────────────────────────────────┐
        │ Layer 3: Vision 工具注册（放大镜） │
        │ - 永远注册 file_read（普通文件读，│
        │   主模型若多模态可直接吃图）       │
        │ - 单模态主模型时：注册 analyze_   │
        │   image / annotate_image（让     │
        │   agent 主动用专家工具看图）      │  ← task-04
        │ - 多模态主模型时：仍注册 analyze_ │
        │   image / annotate_image（精细  │
        │   分析场景 — "用专家做精细分析"） │
        │ - 内部强制走 vision 模型独立分析， │
        │   结果以**文字**形式返回主模型    │
        └────────────────────────┬───────────┘
                                 │
                                 ▼
        ┌────────────────────────────────────┐
        │ buildAgentInput 拼装消息          │  ← task-03
        │ + system prompt + tools           │
        └────────────────────────┬───────────┘
                                 │
                         调 aiservice.Chat
                                 │
                                 ▼
        ┌────────────────────────────────────┐
        │ Layer 4: Runtime error fallback   │  ← task-05
        │ - 若 provider 返回 multimodal-not-│
        │   supported 错误 → 自动剥图重试   │
        │ - 最后兜底，理论上前 3 层不会让走到│
        └────────────────────────────────────┘
```

## Task 列表

| # | Task | 工期 | 依赖 |
|---|---|---|---|
| **1.1** | capability matrix 重构（DB 字段 + 路由 helper） | 1-1.5d | — |
| **1.2** | attachment 双模态固化（异步生成 vision_description + ocr_text + text_fallback） | 1.5-2d | — |
| **1.3** | buildAgentInput capability-aware 路由 | 1d | 1.1 + 1.2 |
| **1.4** | Vision 工具实现（放大镜 — `analyze_image` + `annotate_image`，单/多模态主模型都注册） | 1.5d | 1.1 |
| **1.5** | Runtime 错误剥离重试 | 0.5-1d | 1.3 |

**板块总工期**: 5-8 工作日

## 关键设计决策

### D1: Capability 字段放在哪？

**选项 A**: 加在 `ai_service` 表 `capability_json` 字段（已有 JSON 字段）
**选项 B**: 新建 `ai_service_capability` 关联表

**推荐 A** — 已有字段，零 migration cost。

### D2: VLM 副模型选哪个？

**候选**:
- qwen3-vl-flash（已接入，价格便宜）
- qwen-vl-plus（已接入，质量高）

**推荐 qwen3-vl-flash** — 描述用，flash 够用，省钱。

### D3: text_fallback 格式怎么写？

模板：
```
[图片：{filename}（{width}x{height}，{filesize}KB）。当前模型不支持直接看图，以下是该图的文字描述：

画面描述：
{vision_description}

OCR 提取的文字：
{ocr_text}
]
```

### D4: 异步 fallback 生成失败怎么办？

**策略**：fallback_ready=false + fallback_error 字段记录原因。当用户用单模态模型时，**阻塞 1-2s 等待**，超时则注入 `[图片：{filename}，描述生成失败：{error}]`。

## 验证场景（板块整体级 - 各 task 还有自己的 S5）

1. **多模态 happy path**：用 qwen-vl-flash 上传图 → 看到正常多模态回复
2. **单模态 fallback path**：上传图 → 切到 GLM 5.1 → 看到"agent 描述了图片内容"
3. **会话切换混合**：先 qwen-vl 上传 → 中途切 GLM 5.1 继续问题 → 历史里的图被正确替换为 fallback
4. **fallback 生成失败**：mock VLM 失败 → 用户阻塞 1-2s → 看到"描述生成失败"提示
5. **Runtime 兜底**：人为绕过 Layer 2/3 直接发图给单模态 → Layer 4 自动剥图重试

## 风险

- ⚠️ **VLM 副模型成本**：每张图都跑一次 VLM = 钱。需要监控成本，可能要加每用户配额。
- ⚠️ **fallback 生成延迟**：异步设计，但用户立刻就用单模态模型时可能要等 1-3s。
- ⚠️ **OCR / VLM 中文质量**：实测验证 qwen3-vl-flash 对中文 OCR + 描述的质量是否足够。
