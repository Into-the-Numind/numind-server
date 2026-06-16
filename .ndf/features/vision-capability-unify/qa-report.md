# QA Report — vision-capability-unify

## 验证环境
- 后端：本地（单测）+ dev（端到端，部署后）
- 改动：仅 numind-server（admin-web schema 驱动零改动）

## 自动化检查结果

| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go test（capability+profile+下游）| `go test ./internal/pkg/aiservice/... ./internal/numind/biz/multimodal/... ./internal/numind/biz/agent/...` | PASS | 17 ok / 0 FAIL |
| go vet | `go vet ./...`（改动包）| PASS | |
| golangci-lint（改动包）| exit 0 | PASS | |
| migration 预览 | dev SELECT 预览四步影响行 | PASS | 语法 OK + blast radius 合理（见下）|

**核心 SOT 语义持久回归（Go 单测）**：
- `TestProjectCapabilities_SOT`（capability）：input_modalities 含 image→能看图 / 仅 text→否 / 缺失/空→否 / **accepts_image_inline=true 但无 image→否（退役锁）** / 大小写不敏感 / pdf 独立。
- `TestMatch_LLM_VisionViaInputModalitiesOnly`（profile）：去 features.vision 后模型仅靠 input_modalities=image 即匹配识图任务；非视觉模型不匹配。
- `TestLLMSchema_VisionViaInputModalities`：capabilities enum 不含 vision + input_modalities 标"唯一生效"。

## Migration 预览（dev 实测，read-only）
- **(a1)** 0 行；**(a2)** 4 行（claude-sonnet-4-6-thinking / gemini-3.1-pro / gpt-5.4 / gpt-5.5：有 accepts_image_inline、input_modalities 仅 text → 追加 image，真相搬进 SOT）
- **(b)** 4 个识图任务去 features.vision（sop.vision / salesrag.profile / salesrag.chatstyle / **attachment.vision_describe**——后者确实有 features:[vision]，防御性 WHERE 正确覆盖）
- **(c1)** qwen3-vl-flash + agnes 去 capabilities.vision

## 浏览器/端到端 QA —— 移至 dev（部署后）
**诚实声明**：本 feature 改的是能力判断逻辑（投影派生），核心由 Go 单测持久覆盖。真正的"治本验收"（admin 只勾 input_modalities=image → chatbot 真识图）需在 dev 实跑（依赖真实模型 + COS）。

### dev 端到端清单（S6，**严格按部署时序**）
**⚠ 部署顺序（S3 review P0）**：① ndf-done merge develop → ② **先在 dev 旧容器 DB 跑 migration (a)(b)(c1)**（旧代码读 accepts_image_inline，向后兼容）→ ③ **再部署 T1 新镜像**（新容器 fresh cache 读已回填的 input_modalities）→ ④ 验收。**绝不能先部署后迁移**（否则 seed qwen-vl/手设模型瞬间失明）。
1. migration 后核对：所有真·视觉模型 input_modalities 含 image（text 没丢）；识图任务 requirements 无 vision feature；qwen3-vl-flash/agnes 的 capabilities 无 vision。
2. **P0 回归**：抽查一个原 accepts_image_inline-only 模型（如 gpt-5.5）→ 退役后仍判"能看图"（chatbot 选它上传图能识别）。
3. **治本验收**：admin 给一个**原本纯文本**的模型只勾 input_modalities=image（不碰隐藏字段）→ chatbot 选它上传图 → 真识图；取消勾选 → 回到不能看图。
4. 回归：原视觉模型 chatbot 上传图仍正常；纯文本对话不变。
5. admin 表单：capabilities 不再显示 vision（旧数据清理后无 unknown chip）。
6. **agnes 确认**：agnes（0元默认）配置标了 image → 现被当识图模型。请确认 agnes 是否真该识图；不该则 admin 取消勾选 image（这正是治本后的可运营性）。

## 结论
**ALL_PASS（自动化层）** — SOT 语义/路由/migration 逻辑有持久回归 + dev 预览验证，无 P0。端到端治本验收移至 S6 dev（按严格部署时序）。

## 失败项修复要求
无。
