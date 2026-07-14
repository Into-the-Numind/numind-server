# ADR 0014: 飞书个人工作空间一期交付边界

- Date: 2026-07-14
- Stage: S4 scope amendment
- Status: accepted

## Context

原 S3 计划将可用闭环、生产增强、遗留迁移、完整前端 E2E 与最终验收拆成 23 个 S4 原子任务。用户确认应分两期，要求先完成能让真实用户安全使用的核心闭环，而不是把所有增强都作为一期的前置条件。

## Decision

一期保留每用户独立连接、加密 vault、固定受控 lark-cli、Docs/Base/Wiki 创建/读取/更新、按需授权、原 tool call 自动恢复、最小连接 UI、后端恢复集成、关键 Playwright E2E、S4 质量 Gate 与 S5 本地真实账号验收。

一期不降低以下承诺：不共享或泄露凭据；不注册 IM、删除、raw API 或 shell；重复 resume 不产生第二次飞书业务副作用；删除会话后停止飞书写操作；授权完成后恢复原 tool call 而不是追加伪造用户消息。

Task 14 的旧明文 HOME 迁移、Task 15 的完整可观测性、Task 20 的遗留文件彻底删除，以及 Task 22 的完整 UI 状态/键盘/视觉矩阵移入二期新 feature。若发布前检测到 live 明文 HOME，或旧路径仍可进入 production graph、暴露 IM/broad auth，则该项自动成为一期 blocker，不能以拆期规避。

## Consequences

- 当前 feature S4 进度总数为 20；Task 24 仍是一期 S5 本地真实飞书 Gate，不计入 manifest progress。
- 一期的 Task 22 保留关键 Playwright E2E，满足 NDF 本地验证要求；完整矩阵进入二期。
- 二期另行走 Standard S0–S7，保留明确 backlog，不在一期结束时遗忘。
