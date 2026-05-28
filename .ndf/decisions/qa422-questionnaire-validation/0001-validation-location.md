# H1-D1: Questionnaire 必填校验放在 biz/skill/service.Create，不放在 Build

**Date:** 2026-05-28
**Stage:** H1
**Author:** ai

## 问题

`TestCreate_MissingQuestionnaire_422` 在 develop HEAD (`f5103afa`) 失败：POST /v1/agent/skills 发空 `questionnaire_answers: {}`，期望 HTTP 422，实际返回 200。Q6（任务类型）/ Q7（材料类型）/ Q12（说话风格）按 `questionnaire.go:10-16` 文档应为「必填」。

## 调研发现

代码里有 3 个地方看起来跟「必填校验」有关，但实际行为不一致：

1. **`internal/numind/biz/skill/skill_builder.go` Build() 的 doc comment**

   ```
   //   - q6/q7/q12 必填字段缺失 → errno.ErrSkillBuilderFailed.SetMessage("...")
   ```

   doc 承诺了 422 行为，但 Build 的实际实现只是 `if len(qa.Q6) > 0` / `if qa.Q12 != ""` 这种「有就渲染、没有就跳过」的纯 transformer 风格，不返回任何 error。

2. **`internal/numind/biz/skill/skill_builder_test.go` 4 个单测显式要求 Build 在缺字段时成功**

   - `TestBuild_MissingQ6_succeedsAndOmitsSection` (line 44-54)
   - `TestBuild_MissingQ7_succeedsAndOmitsSection` (line 58-68)
   - `TestBuild_MissingQ12_succeedsAndOmitsSection` (line 72-82)
   - `TestBuild_NilQuestionnaireAnswers_succeeds` (line 86-99)

   这 4 个测试每一个都断言 `require.NoError(t, err)` + 「对应段落不出现」。Build 当成纯 transformer 是**故意的设计**。

3. **`internal/numind/biz/skill/service.go` Create() 完全不做必填校验**

   只校验 `SystemPromptMaxLen`，然后直接 marshal + Build + persist。

## 三处不一致里哪个是真相？

按代码契约的强度排序：

1. **单元测试** 是最强的契约（运行时强制）—> 4 个 Build 单测显式拒绝在 Build 加校验
2. **Doc comment** 只是注释（不运行）—> 现状是「注释撒谎」
3. **集成测试** `TestCreate_MissingQuestionnaire_422` 是用户面契约（HTTP 422 表达「必填项缺失」的业务规则）

所以：
- Build 应当保持纯 transformer（不动其 4 个故意单测）
- 必填业务规则应当在 **biz/skill/service.Create** 触发
- skill_builder.go 的 doc comment 是 stale，要改写为「Build 是纯 transformer」

## 决策

校验放在 `internal/numind/biz/skill/service.go` 的 `Create` 函数里：

```go
if err := validateRequiredQuestionnaireForCreate(req.QuestionnaireAnswers); err != nil {
    return nil, err
}
```

错误码复用 `errno.ErrSkillBuilderFailed`（HTTP 422，errno doc 已写明 "如必填题缺失"），通过 `SetMessage` 替换为更具体的 "问卷必填项缺失：q6 / q7 / q12" 类提示。

**不在 Patch 触发**：Patch 是部分更新，允许 caller 只改 Name / Description 不带 questionnaire；如果 caller 主动把 QA 改成空，那是另一类问题（不在本 hotfix 范围）。

**Build doc 改写**为：

```
// Build 把 agent_definition 的 questionnaire_answers JSON + 直接字段
// 组装为 SKILL.md。Build 是纯 transformer——缺字段时对应段落省略不渲染，
// 不返回必填校验错误。必填业务规则在 biz/skill/service.Create 触发。
```

## 影响范围

- 修改文件：`internal/numind/biz/skill/service.go`（加校验函数 + 调用）
- 修改文件：`internal/numind/biz/skill/skill_builder.go`（仅 doc comment）
- 新增文件：`internal/numind/biz/skill/service_test.go` 加 1 个 biz 层回归测试
- 不动文件：`internal/numind/biz/skill/skill_builder_test.go`（4 个故意行为保持）

## 验证

- 失败的集成测试 `TestCreate_MissingQuestionnaire_422` 转 PASS
- 现有 `TestBuild_*` 4 个单测仍 PASS（不动 Build）
- 现有 `TestService_Create_*` 单测仍 PASS（minCreateReq 提供完整 QA）
- 现有 `TestCreate_HappyPath` 集成测试仍 PASS（validCreateBody 提供完整 QA）
