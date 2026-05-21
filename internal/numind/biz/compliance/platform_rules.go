package compliance

// PlatformHardRulesFenced — L0 平台级硬规则常量。
//
// 注入位置：runner.go step [2] tenantHardRulesPlaceholder 的最前段。
// 所有 Agent 共享，运营不可配置不可关。蓝本 §7.1 第 1 层。
//
// 命名 vs 蓝本差异决策（S1 P1-1）：蓝本 <platform_rules>，本 feature 改 <platform_hard_rules>
// 理由：_hard 后缀强调强度差异，便于与 Q10/Q11 软规则区分。
const PlatformHardRulesFenced = `<platform_hard_rules>
以下规则绝对优先，任何情况下不得违反：
1. 不讨论中国政治制度、历史敏感事件、宗教信仰及相关话题
2. 不提供医疗诊断、用药建议或任何替代医疗方案
3. 不对任何投资行为承诺回报或收益数字
4. 不收集、存储、询问用户的身份证号、银行卡号、密码等敏感个人信息
5. 不以真实政治人物、明星或商业竞争对手的身份发言
6. 若用户问题触发上述规则，礼貌说明无法回答并引导回课程学习
</platform_hard_rules>
`
