# 加量包购买支付弹窗（前端）

## 来源
- 提出人：zchen27@tulane.edu（产品 Owner）
- 提出日期：2026-04-20

## 需求描述

> 在用户"设置"页面，如果具备条件（是新机制下的会员身份），点击加量包之后，应该弹出一个支付弹窗，这个支付弹窗可以让用户扫码支付（微信或者支付宝），这两个支付方式都是已有的代码逻辑，当用户支付成功后，需要给用户充值一个加量包。

## 业务目标

加量包（¥29.9 / 600 积分 / 90 天有效期）是 credits-system 的核心增购通道。当前 `BoosterPurchaseCard` 按钮四态已就绪，但点击后仅弹出 "即将上线" 的 toast（`SettingsView.vue:296`），用户**无法自助完成购买**。

完成此功能后：
- credits 制在期会员可在设置页一键购买加量包（微信 / 支付宝扫码）
- 支付成功后加量包自动到账（复用已验证的 `RechargeWithOrderTx` 链路）
- 账户中心积分余额自动刷新
- 支付失败 / 取消 / 超时场景有清晰反馈

## 优先级

**高**。credits-system 已 S7-done，加量包后端链路完整（订单创建 + 微信 NativePrepay + 支付宝 PagePay + 回调 fulfillOrder + 下发 CreditPackage），但前端购买入口处于 stub 状态，导致整条商业化链路断档。

## Triage
- 推荐轨道：**Standard（轻量版）**
- 分类理由：
  1. 数据库 schema 变更：**否**（复用 `order` + `credit_package`）
  2. 新增 API 端点：**可能 1 个**（订单状态查询接口，待 S1 确认是否已有）
  3. 新外部服务集成：**否**（微信 / 支付宝 SDK 已集成）
  4. 影响文件数：**预估 3–5**（新建 1 PaymentQRModal 组件 + 修改 SettingsView + BoosterPurchaseCard + 可能新增 order API wrapper）
  5. 高风险业务逻辑（支付 / 权限）：**是**（真钱支付流水 + 订单状态机）
- 人类决定：**确认 Standard 轻量版**（S0/S1 快速过，重点在 S2 状态机设计 + S4 严谨编码）

## 边界 / 非目标（S1 可能调整）

**In scope：**
- credits 制 + 在期会员可购买加量包（复用 `BoosterPurchaseCard` 四态，仅 credits 态允许点击）
- 微信 Native（扫码）+ 支付宝 Page（扫码或跳转）两种方式
- 支付成功后积分余额实时更新

**Out of scope（不做）：**
- 不改动后端支付 / 回调 / 加量包下发逻辑
- 不支持老用户（`legacy_tier`）购买加量包（业务规则：booster 仅限会员）
- 不做支付失败后的自动重试
- 不做订单历史页面（已有 admin 端报表，C 端不展示）

## 关键未决问题（S1 探索）

1. 后端是否已有"查询订单支付状态"的用户端接口？前端轮询策略（间隔 / 超时阈值）？
2. 微信 Native Pay 返回 code_url 需要前端 QR 库（qrcode / vue-qrcode）——是否已有依赖？
3. 支付宝 PagePay 返回 HTML form——弹窗内 iframe vs 新开 tab 的 UX 选择？
4. 订单未支付用户关闭弹窗后，后端订单怎么处理（保持 pending 直至过期 vs 立即取消）？

## 备注

- 后端链路已由 credits-system 的 S7 验证（Langfuse + E2E 9/9 pass），本功能**严禁修改后端支付 / 回调代码**
- B2B2C 规则：C 端可自购 booster（Q1.5 已确认，`WithInternalCaller` 仅针对会员类订单限购）——无需额外权限逻辑
- 参考既有 XhsBindModal 的 QR 代码（但那是绑定场景，不是支付；仅作 UI 参考）
