# 公共图片归一化服务（image-normalize-service）

## 来源
- 提出人：用户（2026-06-17 设计讨论）
- 触发：chatbot 用 opus-4-6 上传超大图（「有数表头」**10982×1285px**）→ 服务商返回 `HTTP 400: image dimensions exceed max allowed size: 8000 pixels` → 整条回答失败、无回复。

## 需求描述（用户原话精炼）
> "按这个开干。不过能不能把缩图策略变成一个公共服务，之后所有相关的都能够调用？salesrag 里有缩图逻辑，1. 有没有需要优化的地方？2. 能不能直接接来用 / 变公共服务被未来其它服务接？"

把"图片归一化（缩放/压缩到服务商可接受的尺寸与大小）"做成**一个公共服务**，chatbot/agent 上传、salesrag、VLM 转文字 fallback 等所有相关方统一调用。修复超大图发不出去的问题。

## 业务目标
1. 修复：超大图（单边 >8000px 或体积过大）经 dmxapi 发给识图模型被 400 拒、导致 chatbot/agent 无回复。
2. 治本：图片归一化逻辑收敛为**单一公共服务**，消除 salesrag 的 4 段重复 + 画质 bug + Doubao 写死，未来新接入方零成本复用。
3. 传输改 base64（更通用、去 COS 可达性依赖）——见下方决策。

## 取证（已调研）

### 现状失败
- chatbot/agent 的 inline 识图路径把 COS 预签名 URL（原图）发给 dmxapi → 服务商硬性 8000px/边上限 → 超大图被 400 拒 → `ChatStream` 返回错误 → 用户无回复。错误还**没被映射成友好提示**（日志 "API error unmapped"）。

### salesrag 现有缩图（4 段，需优化，不可照搬）
`biz/salesrag/salesrag.go` 2413/2880/3131/3247 共 4 段，用 `github.com/disintegration/imaging`(Lanczos)+`jpeg.Encode`：
- **缺单边像素上限**（致命）：只检查总像素(36MP)+长宽比(150:1)，不检查单边宽/高。10982×1285≈14MP<36MP 会被放行，但宽 10982>8000px 仍被拒 → salesrag 这套治不了本问题。
- **阈值写死火山 Doubao**（10MB/36MP/150:1），换 claude/qwen/gpt 各家不同，没法直接复用。
- **4 段重复**，阈值还略有出入。
- **画质 bug**：循环里反复 `imaging.Resize(img,...)` 缩已缩过的图，Lanczos 伪影叠加（Claude Code 强调每次从原图缩）。
- 迭代逻辑绕。

### Claude Code 参考（已读源码 `~/Downloads/ClaudeCode/src`）
- 阈值（`constants/apiLimits.ts`）：硬限制 = base64 ≤ **5MB**（目标原始 ≤ 3.75MB）；单边 cap = **2000px**（注释：Anthropic 服务端自动缩 >1568px、不报错，故客户端 cap 2000 只为保质——但这是 Anthropic 一方特性，**别的服务商/我们的 dmxapi 不自动缩，必须客户端缩**）。
- 算法（`utils/imageResizer.ts:maybeResizeAndDownsampleImageBuffer`）：快路径(≤3.75MB且≤2000²原样)→ 尺寸 OK 但太大先压缩(PNG调色板→JPEG 80/60/40/20)→ 尺寸超等比缩(fit:inside,withoutEnlargement)→ 仍大则渐进降质→1000px→400px@q20 → 全失败抛**友好错误**。**每次都从原 buffer 缩**（防伪影叠加）。

## 优先级
高（线上 chatbot 识图直接失败 + 设计债）

## Triage
- 推荐轨道：**Standard**（用户已确认"开干"）
- 分类理由：
  1. 数据库 schema 变更：否
  2. 新增 API 端点：否
  3. 新外部服务集成：否（imaging 依赖已在仓库）
  4. 影响文件数：>3（新 imageutil 包 + upload + salesrag 4 段迁移 + 发图路径 base64 + 适配器确认 + 错误映射）
  5. 高风险业务逻辑：部分（动 salesrag 已上线的图片分析路径 + chatbot 发图路径 + 计费相关的 image token）
- 人类决定：**Standard，开干**（2026-06-17）

## 拟定方案（S2 细化）
1. **公共包 `internal/pkg/imageutil`**：`Normalize(data, opts) (bytes, mediaType, dims, error)`，opts={MaxWidth,MaxHeight,MaxBytes,MaxTotalPixels,MaxAspectRatio}（0=不限）。算法融合 Claude Code（快路径/从原图缩/渐进压缩/友好错误）+ salesrag 可取部分（总像素/长宽比）+ 修画质 bug。用已有 imaging 依赖。**含单元测试**（各阈值边界、超宽图、保 text、迭代收敛）。
2. **chatbot/agent 上传归一化**：`UploadService.Upload` 调 imageutil.Normalize 到**通用安全尺寸（2000px/3.75MB）**，缩好再存 COS——一张图通吃所有服务商，同时修 inline + VLM-describe 两条路。
3. **传输改 base64**：inline 发图把归一化后的图编码成 `data:<mime>;base64,...` 填进 image_url（OpenAI 兼容，**S2 确认适配器/ dmxapi 收 data-URL**），去掉 COS 可达性 + 预签名过期依赖。
4. **迁移 salesrag**：4 段缩图改调 imageutil.Normalize（传各自限制或通用），4 段合一。
5. **错误映射**：服务商图片尺寸/大小 400 → 友好提示（"图片过大，请换小图"）兜底。

## 边界 / 风险
- 仅 image（pdf/audio 另说）。
- salesrag 是已上线路径，迁移后必须回归（图片分析 + OCR 路径）。
- base64 增大发给 dmxapi 的请求体（5MB 封顶）+ 可能增 image token——归一化要够小（2000px/3.75MB）控制成本。
- 归一化在上传时做一次（通用安全尺寸），避免每次发送重复缩 + 多次 COS 拉取。
- 并发：活跃 feature document-editor-ux(S0,numind-server) 与本 feature 文件应不重叠，S4 前核对。
