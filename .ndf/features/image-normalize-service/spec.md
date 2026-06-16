# 公共图片归一化服务 — S1 提案 + S2 技术设计

> feature `image-normalize-service` · Standard · 2026-06-17 · 仅 numind-server

## §1 提案（为什么）

**问题**：chatbot/agent 上传超大图（如 10982×1285px），inline 路径把原图（COS URL）发给识图模型 → dmxapi 硬性 8000px/边上限 → HTTP 400 → 无回复。salesrag 另有 4 段重复的缩图逻辑（写死 Doubao 阈值、缺单边上限、有画质 bug）。

**目标**：把图片归一化做成**单一公共服务**，所有相关方（chatbot/agent 上传、salesrag、VLM 转文字）统一调用；修超大图失败；消除 salesrag 重复。

**传输决策（调研后修正）**：用户初议 base64，调研发现 base64 有**每轮重发 + 计费不确定 + 33% 膨胀**代价，且 URL 路径本就可用（claude 成功拉取了 COS 图、仅因尺寸被拒）。故**改为：URL 传输（发归一化后的 COS 图）+ base64 data-URL 仅作 COS 不可达兜底**（复刻 salesrag 已上线的 `data:image/jpeg;base64,...` 模式，`salesrag.go:2976`）。用户 2026-06-17 拍板选此方案①。

**关键利好**：归一化在**上传时**做（存归一化后的图到 COS），则 inline 发图路径（`multimodal.go` 不改）、VLM 转文字（`fallback_service.go` 不改）**自动拿到安全图**，改动面大幅缩小。

## §2 技术设计

### 2.1 公共包 `internal/pkg/imageutil`（核心，新增）
```go
package imageutil

type Options struct {
    MaxWidth, MaxHeight   int   // 单边像素上限（0=不限）。claude 通用安全=2000
    MaxBytes              int   // 目标体积上限（0=不限）。通用安全=3.75MB(3932160)
    MaxTotalPixels        int64 // 总像素上限（0=不限）。Doubao=36_000_000
    MaxAspectRatio        float64 // 长宽比上限（0=不限）。Doubao=150
}

type Result struct {
    Data      []byte
    MediaType string  // "image/jpeg"|"image/png"|...
    Width, Height int
    Resized   bool    // 是否实际改动（快路径=false）
}

// Normalize 解码 → 判超标 → 等比缩到单边/总像素上限 → 迭代 JPEG 降质到 MaxBytes → 友好错误。
func Normalize(data []byte, opt Options) (Result, error)
```
**算法（融合 Claude Code 正确做法 + salesrag 可取部分 + 修 bug）**：
1. `imaging.Decode` 全解码（不能用只读 `DecodeImageDimensionsFromBytes`）；拿 `bounds.Dx/Dy`。
2. **快路径**：所有限制都满足（含 MaxBytes）→ 原样返回 Resized=false。
3. 算目标尺寸：超单边按比例缩；超 MaxTotalPixels 按 `sqrt` 缩；超 MaxAspectRatio 进一步压（salesrag 长截图特判）。**每次都从原始 decoded img 缩**（修 salesrag 反复缩已缩图的画质 bug）。
4. `imaging.Resize(原img, w, h, Lanczos)` → `jpeg.Encode(quality)`，迭代降质 85→…→20 + 必要时再缩，直到 ≤MaxBytes。
   - **（S2 review P2）目标尺寸守卫**：`int(float64(w)*scale)` 可能截断为 0 → 算完 targetW/targetH 后 `if targetW<=0 {targetW=1}`（targetH 同），防迭代卡 0。
5. 仍不达标（极端）→ 返回 `ErrImageTooLarge`（友好文案，调用方映射给用户）。
- PNG 透明：本期统一转 JPEG（识图够用；保透明留 follow-up）。**（S2 review P2）透明区 jpeg.Encode 填黑**——业务场景（微信聊天截图/文档表头）无透明，已评估接受；带透明 logo 留 follow-up。
- **单测**：超单边图（10982×1285→缩到2000宽）、超总像素、超长宽比、超体积迭代收敛、快路径不动、损坏数据报错、保宽高比。

### 2.2 上传时归一化（`biz/attachment/upload.go` `Upload`）
- 注入点：MIME 嗅探（`mimeType`, line 112）之后、COS 上传（`util.UploadBytesToCOS`, line 154）之前。把 modality 检测提前；若 `modality==image` → `res := imageutil.Normalize(data, 通用安全Options)`；用 `res.Data`/`res.MediaType` 替换 `data`/`mimeType` 再上传 COS。
- `DecodeImageDimensionsFromBytes`（line 171）改在归一化**后**调用（或用 `res.Width/Height`），记录归一化后真实尺寸。
- **（S2 review P1-A）`fileSize` 重算**：upload.go:165 `fileSize := int64(len(data))` 必须在替换 `data=res.Data` **之后**重赋值，否则 DB `AgentAttachment.Size` 存的是原图大小、误导运营/计费。
- 归一化失败：log + 用原图兜底上传（不阻断上传；inline 时再由错误映射兜底），或按 Options 宽松（不抛错只尽力）——S4 定：上传路径**不抛错**，归一化失败就存原图。
- **通用安全 Options**：`{MaxWidth:2000, MaxHeight:2000, MaxBytes:3.75MB}`（2000<8000 通吃 claude；满足 Doubao 36MP；体积控 base64 膨胀）。

### 2.3 传输：URL 主 + base64 兜底
- **主路径不变**：`multimodal.go`/`agent/multimodal.go` 的 `presignAttachmentURL`+`mkInlineBlock` 照常发 COS 预签名 URL——现在 COS 上是归一化后的安全图，直接可用。**这两个 dedup 镜像本期无需改**（归一化在上传时已完成）。
- **base64 兜底（小）**：`presignAttachmentURL` 当 COS 未启用/presign 失败时，当前返回 `att.URL`（可能不可拉取）。增强为：失败时下载 bytes→base64 data-URL。**两份镜像同步改**（multimodal.go + agent/multimodal.go，顶部 TODO(dedup)）。属边缘健壮性，COS 正常时不触发。
  - 若 S3/S4 评估此项触及 dedup 镜像不划算，可降级为 follow-up（核心价值在 2.1+2.2）。

### 2.4 迁移 salesrag 4 段（`salesrag.go`）
- 段 1（`AnalyzeProfileMultiFiles` 2403）/段 2（`analyzeChatStyleImageStream` 2870）/段 4（`ocrWithVisionModel` 3240）：压缩内核等价 → 全部替换为 `imageutil.Normalize(imageData, Doubao Options{MaxBytes:10MB, MaxTotalPixels:36M, MaxAspectRatio:150})`，用返回 bytes 走各自后续（上传 COS / base64 / VisionAnalyze）。
- 段 3（`OCRAnalyze` 3124）：现"先传原图再条件压缩"双上传 → 解耦为「先 Normalize 再上传归一化结果」，省掉原图垃圾对象。
  - **（S2 review P1-B）`ocrWithBaidu` 接归一化字节的 trade-off（已评估接受）**：归一化后传给 `ocrWithBaidu` 的是 ≤2000px 的图。后果：① 百度 OCR 的"超长图(>8192px)分段识别"在 2000px 下不再触发；② `RecognizeChatText` 用自己 decode 的实际宽度 + **比例**判断气泡左右（`loc.Left/imageWidth`），缩放**不影响正确性**。微信长截图归一化到 2000px 高仍有足够细节，准确率轻微下降可接受。**S4 实现者无需为 Baidu 保留原图分支**（保持架构简洁）。
- **不动** `ocrWithBaidu` 函数体（3211，比例判断不受缩放影响，接归一化字节即可）。
- salesrag 是**已上线路径**，迁移后必须回归（图片分析 + chatstyle + OCR）。

### 2.5 错误映射（`errtranslate`）
- 服务商图片尺寸/大小 400（`dimensions exceed`/`image ... too large`/`exceed max`）→ 映射成友好提示「图片过大，请换一张更小的图片」。兜底归一化漏网的极端图（如某供应商限制比 2000px 还严）。
- **（S2 review P2）实现机制**：provider 错误是 string 无类型化错误 → 在 `ToErrno` 用 `strings.Contains(err.Error(), "dimensions exceed") || (strings.Contains(...,"image") && strings.Contains(...,"too large")) || strings.Contains(...,"exceed max")` 匹配 → 新增 `errno.ErrImageTooLarge`（HTTP 400, Message="图片过大，请换一张更小的图片"）。

### 2.6 不改 / 边界
- `fallback_service.go`（VLM 转文字）：零改动（自动用归一化后的 COS 图）。
- 仅 image；PDF/audio 不动；PNG 透明本期转 JPEG。
- imaging 依赖已在 go.mod（v1.6.2）。

## §3 验证策略（S5）
- **Go 单测（持久回归）**：imageutil.Normalize 全阈值边界（超单边/总像素/长宽比/体积、快路径、损坏、保比例、收敛）；salesrag 迁移后压缩函数单测（若可抽）。
- **dev 端到端**：① chatbot 选 opus-4-6 上传**那张 10982px 横幅图** → 验归一化后能识别、不再 400。② 上传正常小图 → 快路径不变。③ salesrag 图片分析/OCR 回归（上传超大微信长截图仍正常）。④ 错误映射：构造超限场景看友好提示。
- 改 salesrag 已上线路径 → 关键迁移必须 Go 单测 + dev 实跑双重覆盖。

## §4 涉及文件预览（S3 细化）
- 新增：`internal/pkg/imageutil/*.go` + `_test.go`
- 改：`biz/attachment/upload.go`（注入归一化）、`biz/salesrag/salesrag.go`（4 段迁移）、`errtranslate`（错误映射）
- 可选改（base64 兜底）：`biz/multimodal/multimodal.go` + `biz/agent/multimodal.go`（dedup 镜像同步）
- 不改：`fallback_service.go`、adapter（data-URL 已支持，已确认）
