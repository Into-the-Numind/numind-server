# QA Report — image-normalize-service

## 验证环境
- 后端：本地（单测）+ dev（端到端，部署后）。仅 numind-server。

## 自动化检查结果
| 检查项 | 命令 | 结果 | 备注 |
|--------|------|------|------|
| Go build 全模块 | `go build ./...` | PASS | T3 salesrag 跨包编译通过 |
| go vet | `go vet`（改动包）| PASS | |
| Go test imageutil | `go test ./internal/pkg/imageutil/` | PASS | 9 用例（超宽10982/总像素/长宽比/字节收敛/不可能预算/损坏/空/快路径/clampMin1）|
| Go test attachment | `go test ./internal/numind/biz/attachment/` | PASS | |
| Go test errtranslate | `go test ./internal/numind/biz/errtranslate/` | PASS | 既有 10 + 新 ImageDimensionsExceed 无 regression |
| Go test errno | `go test ./internal/pkg/errno/` | PASS | |
| golangci-lint（改动包）| exit 0 | PASS | |

> salesrag 既有 credit 测试因 `company_name` test-DB schema 漂移**预先失败**（T2 baseline 已复现），**非本 feature regression**。salesrag 图片函数本身无 Go 单测（迁移靠 dev 实跑回归）。

**双 Sonnet review（T1-T4）双 PASS_WITH_CONCERNS，0 P0 / 0 P1**；3 P2 修 2（aspect-ratio best-effort 注释 / errtranslate 测试 grounding）+ 接受 1（OCRAnalyze→ocrWithVisionModel 双重 normalize = fast-path no-op + 防御性）。代码质量 reviewer 逐段确认 T3 salesrag 4 段**行为等价、上线行为未改坏**。

## 端到端 QA —— dev（部署后）
1. **治本验收**：chatbot 选识图模型（claude-opus-4-6/4-7 或 qwen3-vl-flash）上传一张**超大图**（>8000px 单边，如重新上传那张 10982px 横幅）→ 验**归一化后能识别、不再 HTTP 400**。（注：旧的已存 att 是原图，只对**新上传**生效。）
2. 正常小图 → 快路径，识别正常、画质不降。
3. **salesrag 回归**（上线路径，重点）：销售分析图片上传(AnalyzeProfile)/聊天风格分析(chatstyle)/OCR(OCRAnalyze 含百度+火山视觉) 上传超大微信长截图 → 仍正常出结果。
4. DB 核验：新上传的 agent_attachment.size/width/height 是**归一化后**值（≤2000px、≤3.75MB）。
5. 错误映射（若能构造超 2000px 仍被某模型拒）→ 看到「图片过大，请换一张更小的图片」而非"无回复"。

## 结论
**ALL_PASS（自动化层）** — imageutil 核心算法/上传归一化/错误映射有 Go 单测持久覆盖；salesrag 迁移行为等价（review 逐段确认）。端到端 + salesrag 上线回归在 S6 dev。

## 失败项修复要求
无。
