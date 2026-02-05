package service

import (
	"numind-server/internal/numind/biz/salesrag/domain"
)

// LoadStrategies 加载所有策略数据到内存
// 返回：综合策略列表、基础策略列表
func LoadStrategies() ([]domain.MetaStrategy, []domain.BasicStrategy) {
	metas := loadMetaStrategies()
	basics := loadBasicStrategies()
	return metas, basics
}

// loadMetaStrategies 加载6个综合策略系统
func loadMetaStrategies() []domain.MetaStrategy {
	return []domain.MetaStrategy{
		{
			ID:              "M-T01",
			Name:            "信任建立与证据碾压系统",
			Description:     "处理客户的怀疑、试探、索要证据、抱怨服务等信任类问题",
			TriggerKeywords: []string{"案例", "证据", "不信", "真的吗", "有效果吗", "能证明吗"},
			BasicIDs:        []string{"T-001", "T-002", "T-003", "T-004", "T-005", "T-006", "T-007", "T-008", "T-009", "T-010"},
		},
		{
			ID:              "M-V02",
			Name:            "价值塑造与认知重构系统",
			Description:     "处理客户对产品价值的认知偏差，进行价值重塑和认知升级",
			TriggerKeywords: []string{"价值", "值不值", "有什么用", "为什么", "好处"},
			BasicIDs:        []string{"V-001", "V-002", "V-003", "V-004", "V-005", "V-006", "V-007", "V-008", "V-009", "V-010"},
		},
		{
			ID:              "M-D03",
			Name:            "深度诊断与需求激发系统",
			Description:     "进行客户需求挖掘、痛点诊断和需求激发",
			TriggerKeywords: []string{"需求", "问题", "困扰", "痛点", "想要"},
			BasicIDs:        []string{"P-004", "P-005", "P-007"},
		},
		{
			ID:              "M-C04",
			Name:            "价格守卫与成交锁死系统",
			Description:     "处理价格异议、促成成交、防止客户流失",
			TriggerKeywords: []string{"贵", "便宜", "优惠", "折扣", "价格", "成交", "买"},
			BasicIDs:        []string{"T-008", "T-009", "T-010", "C-001", "C-002", "C-003", "C-004"},
		},
		{
			ID:              "M-R05",
			Name:            "节奏掌控与主线收复系统",
			Description:     "处理对话节奏失控、话题跑偏、客户注意力分散等问题",
			TriggerKeywords: []string{"跑题", "其他", "先不说", "以后再说"},
			BasicIDs:        []string{"C-001", "P-002"},
		},
		{
			ID:              "M-P06",
			Name:            "专业边界与位势建设系统",
			Description:     "处理客户试探边界、建立专业权威、维护服务位势",
			TriggerKeywords: []string{"电话", "语音", "免费", "试试", "先看看"},
			BasicIDs:        []string{"P-001", "P-002", "P-003", "P-006", "P-008", "P-009"},
		},
	}
}

// loadBasicStrategies 加载33个基础策略卡片
func loadBasicStrategies() []domain.BasicStrategy {
	return []domain.BasicStrategy{
		// P系列：专业边界与位势建设
		{
			ID:              "P-001",
			MetaID:          "M-P06",
			Name:            "交付边界与位势重构",
			Description:     "拒绝语音/电话请求，建立高位势",
			TriggerKeywords: []string{"电话", "语音", "打个电话", "聊聊"},
			Content:         strategyContentP001,
		},
		{
			ID:              "P-002",
			MetaID:          "M-P06",
			Name:            "观察切入与话题跃迁",
			Description:     "通过观察客户行为切入话题",
			TriggerKeywords: []string{"关注", "看过"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "P-003",
			MetaID:          "M-P06",
			Name:            "同赛道案例反向背书",
			Description:     "用同行业案例建立信任",
			TriggerKeywords: []string{"同行", "案例", "行业"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "P-004",
			MetaID:          "M-D03",
			Name:            "需求双刃剑提问",
			Description:     "通过提问挖掘真实需求",
			TriggerKeywords: []string{"需求", "想要什么"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "P-005",
			MetaID:          "M-D03",
			Name:            "数据祛魅与风险诊断",
			Description:     "用数据揭示问题本质",
			TriggerKeywords: []string{"数据", "效果"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "P-006",
			MetaID:          "M-P06",
			Name:            "归因错位与信心重塑",
			Description:     "重新归因问题，重建信心",
			TriggerKeywords: []string{"失败", "没效果", "不行"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "P-007",
			MetaID:          "M-D03",
			Name:            "诊断前置定价权守卫",
			Description:     "在诊断阶段预设价值锚点",
			TriggerKeywords: []string{"多少钱", "报价"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "P-008",
			MetaID:          "M-P06",
			Name:            "元能力识别与降维赞美",
			Description:     "识别客户潜力，降维赞美",
			TriggerKeywords: []string{"厉害", "能力"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "P-009",
			MetaID:          "M-P06",
			Name:            "应对客户索要效果承诺",
			Description:     "处理客户要求保证效果的情况",
			TriggerKeywords: []string{"保证", "承诺", "效果"},
			Content:         "策略内容待填充",
		},
		// V系列：价值塑造与认知重构
		{
			ID:              "V-001",
			MetaID:          "M-V02",
			Name:            "泛流量纠偏与精准筛选",
			Description:     "纠正流量认知偏差",
			TriggerKeywords: []string{"流量", "粉丝"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-002",
			MetaID:          "M-V02",
			Name:            "方法论黑箱与命名特权",
			Description:     "包装方法论，建立专属感",
			TriggerKeywords: []string{"方法", "怎么做"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-003",
			MetaID:          "M-V02",
			Name:            "阶段升维与需求重定义",
			Description:     "升级客户认知维度",
			TriggerKeywords: []string{"阶段", "升级"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-004",
			MetaID:          "M-V02",
			Name:            "机会成本与不对称竞争",
			Description:     "强调机会成本",
			TriggerKeywords: []string{"成本", "时间"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-005",
			MetaID:          "M-V02",
			Name:            "成功路径预演与具象化",
			Description:     "描绘成功路径",
			TriggerKeywords: []string{"怎么成功", "路径"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-006",
			MetaID:          "M-V02",
			Name:            "定制化幻觉与模块化组装",
			Description:     "制造定制化感受",
			TriggerKeywords: []string{"定制", "专属"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-007",
			MetaID:          "M-V02",
			Name:            "认知拉升与互惠陷阱",
			Description:     "提升认知并建立互惠感",
			TriggerKeywords: []string{"认知", "学习"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-008",
			MetaID:          "M-V02",
			Name:            "结果前置与领域锚定",
			Description:     "前置结果预期",
			TriggerKeywords: []string{"结果", "目标"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-009",
			MetaID:          "M-V02",
			Name:            "劣势转译与网络优势",
			Description:     "将劣势转化为优势",
			TriggerKeywords: []string{"劣势", "不足"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "V-010",
			MetaID:          "M-V02",
			Name:            "灵魂三问",
			Description:     "为什么做/现在做/找你做",
			TriggerKeywords: []string{"为什么", "现在", "找你"},
			Content:         "策略内容待填充",
		},
		// T系列：信任建立与证据碾压
		{
			ID:              "T-001",
			MetaID:          "M-T01",
			Name:            "饱和视觉证据攻击",
			Description:     "用大量视觉证据建立信任",
			TriggerKeywords: []string{"案例", "证据", "看看"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-002",
			MetaID:          "M-T01",
			Name:            "社会证明信任嫁接",
			Description:     "通过社会证明建立信任",
			TriggerKeywords: []string{"别人", "其他人"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-003",
			MetaID:          "M-T01",
			Name:            "信息节制与引导自证",
			Description:     "控制信息披露，引导自证",
			TriggerKeywords: []string{"隐私", "保密"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-004",
			MetaID:          "M-T01",
			Name:            "响应力碾压与闭环",
			Description:     "通过快速响应建立信任",
			TriggerKeywords: []string{"回复", "响应"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-005",
			MetaID:          "M-T01",
			Name:            "情感账户超额充值",
			Description:     "通过情感投入建立信任",
			TriggerKeywords: []string{"感情", "关心"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-006",
			MetaID:          "M-T01",
			Name:            "结果预判与焦虑植入",
			Description:     "预判结果，植入适度焦虑",
			TriggerKeywords: []string{"后果", "如果不"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-007",
			MetaID:          "M-T01",
			Name:            "案例碾压与借口粉碎",
			Description:     "用案例粉碎借口",
			TriggerKeywords: []string{"借口", "理由"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-008",
			MetaID:          "M-C04",
			Name:            "价格铁律守卫",
			Description:     "坚守价格底线",
			TriggerKeywords: []string{"便宜点", "打折"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-009",
			MetaID:          "M-C04",
			Name:            "风险共担逻辑对冲",
			Description:     "通过风险共担促成交",
			TriggerKeywords: []string{"风险", "担心"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "T-010",
			MetaID:          "M-C04",
			Name:            "优雅拒绝议价",
			Description:     "优雅地拒绝议价",
			TriggerKeywords: []string{"优惠", "少点"},
			Content:         "策略内容待填充",
		},
		// C系列：价格守卫与成交锁死
		{
			ID:              "C-001",
			MetaID:          "M-C04",
			Name:            "框架性提问拉回主线",
			Description:     "用提问拉回对话主线",
			TriggerKeywords: []string{"跑题", "说回来"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "C-002",
			MetaID:          "M-C04",
			Name:            "延迟满足制造期待",
			Description:     "通过延迟满足制造期待",
			TriggerKeywords: []string{"等等", "稍后"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "C-003",
			MetaID:          "M-C04",
			Name:            "零摩擦启动流程",
			Description:     "降低启动门槛",
			TriggerKeywords: []string{"开始", "启动"},
			Content:         "策略内容待填充",
		},
		{
			ID:              "C-004",
			MetaID:          "M-C04",
			Name:            "全款原则与重服务定调",
			Description:     "坚持全款，强调服务",
			TriggerKeywords: []string{"分期", "先付一点"},
			Content:         "策略内容待填充",
		},
	}
}

// P-001 完整策略内容
const strategyContentP001 = `# P-001 交付边界与位势重构

## 触发条件
- 客户直接提出："老师，方便语音聊聊吗？"或"打个电话行吗？"
- 客户带着明确需求进场："两年前就关注你了，想领个资料顺便跟你语音诊断下。"

## 核心目标
- 本回合目标：拒绝即时语音，但将拒绝重构为"对交付的敬畏"
- 战役目标：确立"交付重于拓客"的底线

## 策略选择
策略名称：「交付锁定 + 情绪置换」
拒绝语音请求，但先对客户"想语音"背后代表的"重视度"给予极高评价。

## 话术模板
**第一步：情绪认同与重要性定性**
"[客户昵称]，看到你想跟我语音沟通，我其实特别开心。这说明你对做小红书这件事情是真的做好了准备，而且是非常重视的，这一点我特别认可。"

**第二步：高位拒绝与理由重构**
"只是[客户名]，不是我不重视和你的沟通哈。确实是因为我现在大部分的精力都是放在对加入学员的交付上的，为了对他们负责，前端的语音咨询哪怕是付费我也都没有再接了。"

**第三步：掌控节奏与价值预支**
"虽然不接语音，但你的问题或想法都可以发文字告诉我。我会利用交付间隙抽空仔细回你，而且文字有留存记录，你之后翻看也方便。你发给我，我来帮你拆一下。"
`
