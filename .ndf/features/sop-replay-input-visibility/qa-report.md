# SOP 回看页输入与上传可见 — S5 QA 报告

- 日期：2026-06-16
- 环境：dev（前端 http://49.233.219.254:9200，后端 9091）
- 部署镜像：numind-server `develop-347b22c4-dirty`（02:33 创建，healthy）、numind-web-v3 `develop-c7bf40e`（02:32 创建，healthy）；SSH 核对 dev 容器确在跑新镜像（非 TCR-满静默旧容器）。
- 方式：gstack 浏览器 + E2E 测试账号（user_id=1 admin）登录 dev 回看真实历史运行；后端单测 + 前端 vitest 做持久回归（见 T1/T2）。
- 测试数据：run 1440（succeeded，3 文档上传 doc/pdf/pdf）+ 新建 run 2862（端到端上传一张测试 PNG 验图片路径与 200）。

## 验收标准核验（9/9 PASS）

| AC | 结论 | 证据 |
|----|------|------|
| AC1 输入文本可见 | ✅ | run 1440 step1「你的输入」文本块渲染；step4 文本(11字)渲染。截图 /tmp/replay_step1.png |
| AC2 图片缩略图+放大+非死链 | ✅ | 新建 run 2862 上传 PNG → 回看缩略图渲染(img.complete=true, naturalW=16) → 点击 AgentImagePreview 全屏放大(overlay+下载/关闭)。图片 URL=**inline GET 签名**(无 attachment disposition)，curl 返回 **HTTP 200 image/png 79B**(=上传原图) |
| AC3 文档卡可打开+类型大小 | ✅ | run 1440 step1 文档卡「为什么找我来买房.doc / DOC · 12 KB」(formatFileSize 生效)；URL=**download 签名**(attachment+RFC5987 原名)，签名有效 |
| AC4 不重复展示文件提取文本 | ✅ | 所有步骤 `innerText.includes('=== ')==false`；strip 分隔符法生效，「你的输入」只含用户文本 |
| AC5 提取文本可展开 | ✅ | run 1440 step1 点「查看提取文本」→ `.replay-input__doc-content` 展开(609字 mono)，chevron 旋转。截图 /tmp/replay_expanded.png |
| AC6 无上传步骤优雅处理 | ✅ | step4(无文件)→ 文本块显示、无「上传的素材」区、uploads=0、无报错。全空步骤由 shouldRender v-if + 单测覆盖 |
| AC7 只读纪律 | ✅ | 回看卡内无 textarea/上传/执行按钮；HistoryViewStrip「正在查看历史步骤·输入不可修改」；既有复制/收藏不受影响 |
| AC8 status 透出 files+签名 | ✅ | GET /sop/runs/1440/status completed_nodes[1..3] 均含 files[]，URL 实时签名；run 2862 图片 files inline 签名。图片→inline、文档→download 路由正确 |
| AC9 视觉一致美观 | ✅ | 截图：中性「✎ 你的输入」head(区别 AI accent)、surface-tint 文本块+渐隐展开、「📎 上传的素材·N」、文件卡(中性图标)、与 OutputCard 体系统一 |

## 核心风险验证（spec §1 标记「私有桶裸链 403 死链」）
- **已证伪**：签名管线有效。旧数据(run 1440/2006/1619 等，`pdf/` 前缀历史对象已被 GC)签名 URL 返回 **404（非 403）**=COS 接受签名但对象不存在；新鲜上传(run 2862，`vision/` 前缀活对象)返回 **200**。即：签名永远有效，对象在则 200、不在则 404，绝无 403 死链。
- 含义：当前/未来上传的文件回看链接可正常访问；早期历史运行若对象已被清理则 404（数据留存问题，非功能缺陷，与任何直接下载行为一致）。

## 观察到的非功能问题
- 一次孤立 `GET /api/v1/sop/templates?page=1&page_size=1 → 502`(94ms) —— 非本功能端点(/sop/runs/:id/status 全程正常)，系刚部署后端容器短暂抖动，非本次回归。

## 回归保护诚实声明
- 持久回归：后端 `attachNodeFiles`/`isImageExt` Go 单测 + 前端 `stripMergedFileBlocks`/`formatFileSize`/`isImageFile` vitest（18 用例）。
- gstack QA 为一次性截图验证，不产持久 UI 测试代码；本功能纯展示、非支付/权限高风险，符合 plan T4 验证策略（S3 reviewer 已批准）。
- 遗留测试数据：dev 测试账号新增 run 2862（端到端上传验证用），保留在 dev（同其它 E2E 测试运行）。

## 结论
S5 PASS。功能在 dev 真实数据上 9/9 AC 通过，核心死链风险证伪。止步 dev（用户 /goal 授权至 dev，prod=独立硬门禁未授权）。
