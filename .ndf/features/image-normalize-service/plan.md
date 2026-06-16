# 公共图片归一化服务 — 实施计划（S3）

> feature `image-normalize-service` · 2026-06-17 · 蓝本 spec.md · 仅 numind-server
> code task = T1–T4，T5 验证策略。主 session 顺序实现 + 每 task 双 reviewer。

## 依赖图
```
T1(imageutil 公共包) ──► T2(上传归一化)
                     └──► T3(salesrag 迁移)
T4(错误映射) 独立
T1..T4 ──► T5(S5 验证策略)
```
T1 是地基（T2/T3 都调它）。T4 独立。

---

## T1 — 公共包 `internal/pkg/imageutil`（核心地基）
**涉及文件（新增）**：`internal/pkg/imageutil/imageutil.go` + `imageutil_test.go`

**内容**：
- `Options{MaxWidth, MaxHeight int; MaxBytes int; MaxTotalPixels int64; MaxAspectRatio float64}`（0=不限）。
- `Result{Data []byte; MediaType string; Width, Height int; Resized bool}`。
- `Normalize(data []byte, opt Options) (Result, error)`：
  1. `imaging.Decode` 全解码；`bounds.Dx/Dy`。
  2. 快路径：满足所有限制（含 MaxBytes）→ 原样 Resized=false。**（S3 review P2）MediaType 用 `http.DetectContentType(data[:min(len,512)])` 嗅探**——`imaging.Decode` 返回 `image.Image` 不带 format，没有 `imaging.Format` API（直接查会编译失败）。
  3. 算目标尺寸：超 MaxWidth/Height 按比例；超 MaxTotalPixels 按 `sqrt(maxPx/total)`；超 MaxAspectRatio 进一步压（长截图特判）。守卫 `if targetW<=0 {targetW=1}`（targetH 同）。
  4. **每次从原 decoded img** `imaging.Resize(img,w,h,Lanczos)` → `jpeg.Encode(quality)`，quality 85→…→20 + 必要时再缩，迭代到 ≤MaxBytes。`width>50` 等守卫防死循环。
  5. 仍不达标 → `ErrImageTooLarge`（exported sentinel + 友好文案）。
- PNG 等统一 `jpeg.Encode`（透明填黑，已评估接受）。

**单测**：超单边(10982×1285→缩2000宽,比例对)、超总像素、超长宽比、超体积迭代收敛、快路径不动(Resized=false)、损坏数据报错、保宽高比、ErrImageTooLarge 极端、targetW=0 守卫。

**验收**：`go test ./internal/pkg/imageutil/...` 绿；`task lint` 0。包独立编译。

---

## T2 — 上传时归一化（`biz/attachment/upload.go`）
**涉及文件**：改 `internal/numind/biz/attachment/upload.go` + 配套测试（若可）

**内容**：
- **（S3 review P0-1）注入位置精确**：归一化代码块插在 **line 154 `UploadBytesToCOS` 之前**（line 112 MIME 嗅探之后）。把 `modality := agentatt.DetectModality(mimeType)` 提前到此；若 `modality==agentatt.ModalityImage` → `res, err := imageutil.Normalize(data, imageutil.Options{MaxWidth:2000,MaxHeight:2000,MaxBytes:3932160})`。
  - 成功 → `data = res.Data`；`mimeType = res.MediaType`（统一 image/jpeg）。
  - 失败 → **（P2）log warn + `data`/`mimeType` 均保持原值**（不抛错、不阻断；直接走 line 154 上传原图）。
- 这样 line 154 传的就是（成功后的）归一化 bytes；**line 165 `fileSize := int64(len(data))` 天然就是归一化后大小，无需额外 fixup**（满足 P1-A，去掉"重算"歧义）。
- `DecodeImageDimensionsFromBytes`（line 171）此时读的 `data` 已是归一化后（或用 `res.Width/Height`）。

**验收**：`go test ./internal/numind/biz/attachment/...` 绿；`task lint` 0。**（S3 review P2）先确认 `biz/attachment` 是否有 COS test double**（`util.UploadBytesToCOS` 可否注入/COS 未启用返回空走 /local-uploads 分支）：有则加 Upload 单测验归一化生效；否则归一化生效靠 imageutil 单测(T1)覆盖 + S5 dev 实跑（DB size/dims 核验）。

---

## T3 — 迁移 salesrag 4 段（`biz/salesrag/salesrag.go`）
**涉及文件**：改 `internal/numind/biz/salesrag/salesrag.go`

**内容**（DoubaoOpts = `imageutil.Options{MaxBytes:10*1024*1024, MaxTotalPixels:36_000_000, MaxAspectRatio:150}`）：
- **段1** `AnalyzeProfileMultiFiles`(2403)：压缩内核替换为 `imageutil.Normalize(imageData, DoubaoOpts)` → 用 `res.Data` 走后续。**（S3 review P1-2）归一化失败(ErrImageTooLarge)维持现有 `continue` 语义**（line 2496-2500：log warn + 跳过该图，不改对 caller 行为）。
- **段2** `analyzeChatStyleImageStream`(2870)：仅替换上方（~2868-2956）压缩内核为 Normalize；**（S3 review P2）保留 line 2974-2979 现有「COS 上传失败 → base64 data-URL 兜底」逻辑不动**（这是已上线兜底）。
- **段4** `ocrWithVisionModel`(3240)：压缩内核替换为 Normalize → `res.Data` 走 COS 签名 URL → `volcBiz.VisionAnalyze`。
- **段3** `OCRAnalyze`(3108)：**（S3 review P0-2）现有是"先传原图(3115)+条件压缩传 `_ai.jpg`(displayObjectKey)双上传 + baidu 收原图 imageData(3202)"**。迁移为：① `res := Normalize(imageData, DoubaoOpts)`；② `imageData = res.Data`；③ **单次上传** `res.Data` 到 COS（key=`objectKey`）；④ `frontendURL` 改用 `objectKey` 签名 URL；⑤ **删掉** `maxAIImageSize`/`needsCompress`/`displayObjectKey`/`_ai.jpg` 双上传逻辑；⑥ `ocrWithBaidu` 收归一化后 `imageData`（trade-off 已评估接受，不为 Baidu 留原图分支）。
- **不动** `ocrWithBaidu` 函数体（比例判断不受缩放影响）。
- 删掉迁移后冗余的 `maxVisionSize`/`maxAIImageSize`/迭代压缩死代码。

**验收**：`go test ./internal/numind/biz/salesrag/...` 绿（既有测试不挂）；`task lint` 0。**salesrag 是已上线路径**——S5 dev 必须回归（图片分析/chatstyle/OCR）。

---

## T4 — 错误映射（`errtranslate` + `errno`）
**涉及文件**：改 `internal/pkg/errno/`（新增 `ErrImageTooLarge`）+ `errtranslate` 的 `ToErrno`

**内容**：
- `errno.ErrImageTooLarge`：HTTP 400，Message="图片过大，请换一张更小的图片"。
- `ToErrno`：`strings.Contains(err.Error(), "dimensions exceed") || (Contains "image" && Contains "too large") || Contains "exceed max"` → 返回 `ErrImageTooLarge`。
- 兜底归一化漏网的极端图（某供应商限制比 2000px 还严）→ 用户看到友好提示而非"无回复"。

**（S3 review P1-1）** `ToErrno` 现有是 `errors.As`+`errors.Is` 两路，**新增** strings.Contains 分支（在现有逻辑之后，不替换）。
**验收**：`go test ./internal/pkg/errno/... ./...（errtranslate 所在包）`；`task lint` 0。**现有 translate_test.go 全部测试仍通过（无 regression）** + 新增 `TestToErrno_ImageDimensionsExceed`（含 "dimensions exceed" 的 error → ErrImageTooLarge）。

---

## T5 — S5 验证策略（rule 10）
**验证方式**：Go 单测（持久回归核心）+ dev 端到端（含 salesrag 上线路径回归）。
**理由**：imageutil 核心算法/上传/错误映射有 Go 单测持久覆盖；salesrag 是已上线路径，迁移必须 dev 实跑回归。改图片处理中风险，关键逻辑必须 Go 单测（T1/T4）。非 bug-from-customer（功能修复/重构），无强制复现测试。
**S5 关键路径**：
1. **治本验收**：chatbot 选 opus-4-6 上传**那张 10982px 横幅图** → 验归一化后能识别、不再 400。
2. 正常小图 → 快路径，识别正常、画质不降。
3. **salesrag 回归**：图片分析(AnalyzeProfile)/chatstyle/OCRAnalyze 上传超大微信长截图 → 仍正常（这是上线路径，重点）。
4. 错误映射：（若能构造）超限图看到"图片过大"友好提示。
5. DB 核验：上传后 agent_attachment.size 是归一化后大小、width/height 是归一化后尺寸。

---

## 后续 / 边界
- **base64 兜底（spec §2.3）降级为 follow-up**：归一化在上传时已让 COS 存安全图、URL 路径通；base64 兜底仅 COS 不可达边缘场景，且触及 multimodal dedup 双镜像，本期不做，记 follow-up（salesrag 侧已有 base64 兜底）。
- 并发：活跃 feature document-editor-ux(S0,numind-server) 与本 feature 文件（imageutil/upload/salesrag/errno）应不重叠，S4 前 fetch 核对。
- Tier：T1–T4 文件 disjoint，主 session 顺序实现 + 每 task 并行双 reviewer。
