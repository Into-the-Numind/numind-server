# tech debt #9 fix / Wave 3

**Date:** 2026-04-30

**Feature:** `membership-credits-redesign`

**Migrated from:** `build-manifest.yaml` decisions[]

---

Order.Months 字段为 booster 复用的隐藏耦合解除——新增独立 payment_order.quantity 字段（INT NOT NULL DEFAULT 1）+ 历史数据回填迁移（booster 行 quantity = months where months > 1）。fulfillOrder 读 order.Quantity 路由给 RechargeWithOrderTx（booster 路径），months 只在 monthly/yearly 路径读取。RechargeWithOrderTx booster case 修复：从创建 1 个包改为创建 quantity 个包（每个 600cr 独立 FIFO 行），修复了多份购买仅授予 1 份的隐藏 bug。controller createOrderRequest 新增 quantity 字段（binding min=1,max=10000），model 新增 GetBoosterAmount(quantity) + GetBoosterProductName(quantity) 两个 helper。commits: 83ce6dd (migration+model+biz) / 22b16e4 (tests)
