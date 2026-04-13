package service

import (
	"numind-server/internal/numind/biz/salesrag/domain"
	"os"
	"path/filepath"
)

// LoadStrategies 加载所有策略数据到内存
// 返回：综合策略列表、基础策略列表
func LoadStrategies() ([]domain.MetaStrategy, []domain.BasicStrategy) {
	metas := loadMetaStrategies()
	basics := loadBasicStrategies()
	return metas, basics
}

// loadMetaStrategies 加载6个综合策略系统 (V3.0/V2.0)
func loadMetaStrategies() []domain.MetaStrategy {
	return []domain.MetaStrategy{
		{
			ID:           "M-C01",
			Name:         "价格守卫与成交锁死系统",
			Description:  "守卫价格铁律与全款原则，优雅拒绝议价并锁定高客单价成交。当客户认可价值但针对价格、付款方式、风险承诺提出异议，意图压价或通过分期降低决策风险时触发。",
			DecisionTree: decisionTreeMC01,
			TriggerKeywords: []string{
				"太贵了", "便宜点", "分期", "按效果付费", "去年多少钱", "有赠品吗", "压力大", "能保证吗",
			},
			BasicIDs: []string{"T-008", "T-009", "T-010", "C-004", "P-009"},
		},
		{
			ID:           "M-V01",
			Name:         "价值塑造与认知重构系统",
			Description:  "重塑客户底层认知，将身份升维至老板，建立系统化解决方案价值。当客户迷恋执行细节、因身份错位导致技能焦虑，或用低维参照系衡量服务价值产生认知偏差时触发。",
			DecisionTree: decisionTreeMV01,
			TriggerKeywords: []string{
				"流量", "播放量", "教剪辑吗", "自己先学", "行业特殊", "你在外地", "再想想", "代运营",
			},
			BasicIDs: []string{"V-001", "V-005", "V-003", "V-008", "V-006", "V-009", "V-004", "V-002"},
		},
		{
			ID:           "M-C02",
			Name:         "节奏掌控与主线收复系统",
			Description:  "夺回对话主导权，纠正话题跑偏或细节纠缠，强力拉回成交主航道。当客户陷入非战略细节、话题转向竞对八卦，或因过热冲动及决策焦虑导致沟通效率降低时触发。",
			DecisionTree: decisionTreeMC02,
			TriggerKeywords: []string{
				"字体", "版权", "那个平台怎么样", "现在就付", "下一步干嘛", "链接多少", "还没聊完", "具体操作",
			},
			BasicIDs: []string{"C-001", "C-002", "C-003"},
		},
		{
			ID:           "M-D01",
			Name:         "深度诊断与需求激发系统",
			Description:  "拆除心理防御，精准定位商业卡点与痛点，唤醒深度合作需求。在破冰初期或客户展示现状时，用于处理防御性沉默、虚荣数据或挫败感，建立专业对位并揭示真问题。",
			DecisionTree: decisionTreeMD01,
			TriggerKeywords: []string{
				"你好", "帮看账号", "没效果", "几千赞", "不适合我", "XX家便宜", "我的逻辑", "怎么努力",
			},
			BasicIDs: []string{"P-002", "P-004", "P-005", "P-006", "P-007", "P-008"},
		},
		{
			ID:           "M-T01",
			Name:         "信任建立与证据碾压系统",
			Description:  "通过饱和式证据投喂与超预期响应，击穿怀疑并建立职业可靠性。当客户表现出对真实性、专业性或可靠性的怀疑，索要案例证明或因响应延迟产生负面情绪时触发。",
			DecisionTree: decisionTreeMT01,
			TriggerKeywords: []string{
				"有案例吗", "凭什么帮你", "你是真人吗", "推个学员给我", "打不开", "还没回我", "日更", "看后台",
			},
			BasicIDs: []string{"T-001", "T-002", "T-003", "T-004", "T-005", "T-006", "T-007"},
		},
		{
			ID:           "M-P02",
			Name:         "专业边界与位势建设系统",
			Description:  "建立专家高位与稀缺性，通过设立交付边界防止咨询降级为陪聊。在接触初期，当客户试图打破服务流程、占用即时带宽或通过索取特殊待遇测试商业边界时触发。",
			DecisionTree: decisionTreeMP02,
			TriggerKeywords: []string{
				"电话聊", "语音吗", "简单看下", "给点建议", "在吗", "关注很久了", "破个例", "现在方便吗",
			},
			BasicIDs: []string{"P-001"},
		},
	}
}

// loadBasicStrategies 加载基础策略卡片 (全量映射)
func loadBasicStrategies() []domain.BasicStrategy {
	return []domain.BasicStrategy{
		// M-C01 价格守卫 (T-008, T-009, T-010, C-004, P-009)
		{ID: "T-008", MetaID: "M-C01", Name: "价格铁律守卫", Description: "承认旧价已淘汰，强调当前版本质变", Content: "策略内容待填充"},
		{ID: "T-009", MetaID: "M-C01", Name: "风险共担逻辑对冲", Description: "将分成定义为合伙人模式，咨询费为购买专注力", Content: "策略内容待填充"},
		{ID: "T-010", MetaID: "M-C01", Name: "优雅拒绝议价", Description: "情感阻断升维，高能量指令覆盖博弈", Content: "策略内容待填充"},
		{ID: "C-004", MetaID: "M-C01", Name: "全款原则与重服务定调", Description: "全款是启动多团队饱和支持的前提", Content: "策略内容待填充"},
		{ID: "P-009", MetaID: "M-C01", Name: "效果承诺与风险共担", Description: "引导至共建成功条件的协作框架，展示交付可视化", Content: "策略内容待填充"},

		// M-V01 价值塑造 (V-001, V-005, V-003, V-008, V-006, V-009, V-004, V-002)
		{ID: "V-001", MetaID: "M-V01", Name: "泛流量纠偏", Description: "纠正流量数据迷信", Content: "策略内容待填充"},
		{ID: "V-005", MetaID: "M-V01", Name: "案例刺激/愿景刺激", Description: "Before-After对比，制造紧迫感", Content: "策略内容待填充"},
		{ID: "V-003", MetaID: "M-V01", Name: "阶段升维", Description: "赋予操盘手身份，消除操作焦虑", Content: "策略内容待填充"},
		{ID: "V-008", MetaID: "M-V01", Name: "结果前置", Description: "强项锚定，针对行业特殊性疑虑", Content: "策略内容待填充"},
		{ID: "V-006", MetaID: "M-V01", Name: "模块化处方", Description: "针对行业特殊性的定制化幻觉", Content: "策略内容待填充"},
		{ID: "V-009", MetaID: "M-V01", Name: "劣势转译", Description: "将距离重构为信息差优势", Content: "策略内容待填充"},
		{ID: "V-004", MetaID: "M-V01", Name: "机会成本", Description: "强调拖延的代价", Content: "策略内容待填充"},
		{ID: "V-002", MetaID: "M-V01", Name: "方法论黑箱", Description: "价值隔离与系统定义，应对浅薄理解", Content: "策略内容待填充"},

		// 补充 V-007 (列表中未直接提及，暂时保留或归入V系统)
		{ID: "V-007", MetaID: "M-V01", Name: "认知拉升与互惠陷阱", Description: "互惠感建立", Content: "策略内容待填充"},
		// 补充 V-010 (同上)
		{ID: "V-010", MetaID: "M-V01", Name: "灵魂三问", Description: "为什么做/现在做/找你做", Content: "策略内容待填充"},

		// M-C02 节奏掌控 (C-001, C-002, C-003)
		{ID: "C-001", MetaID: "M-C02", Name: "框架性提问", Description: "零散问题 vs 商业系统二选一拉回战略层", Content: "策略内容待填充"},
		{ID: "C-002", MetaID: "M-C02", Name: "延迟满足与场域跃迁", Description: "踩刹车引向高阶直播/闭门会", Content: "策略内容待填充"},
		{ID: "C-003", MetaID: "M-C02", Name: "零摩擦流程接管", Description: "抛出项目启动任务卡，接管行动", Content: "策略内容待填充"},

		// M-D01 深度诊断 (P-002, P-004...P-008)
		{ID: "P-002", MetaID: "M-D01", Name: "观察切入与话题跃迁", Description: "跳过寒暄，直接认可赛道并询问业务逻辑", Content: "策略内容待填充"},
		{ID: "P-004", MetaID: "M-D01", Name: "需求双刃剑提问", Description: "认可专业，用精准咨询量刺破幻觉", Content: "策略内容待填充"},
		{ID: "P-005", MetaID: "M-D01", Name: "数据祛魅与风险诊断", Description: "指出平台风险，引导思考赞与价值的区别", Content: "策略内容待填充"},
		{ID: "P-006", MetaID: "M-D01", Name: "归因错位与信心重塑", Description: "将失败归因为语言体系冲突，保护自尊", Content: "策略内容待填充"},
		{ID: "P-007", MetaID: "M-D01", Name: "竞对降维隔离", Description: "定义我方为高客单变现派，建立思维对立", Content: "策略内容待填充"},
		{ID: "P-008", MetaID: "M-D01", Name: "元能力识别与降维赞美", Description: "赞美思维能力，提出流量诅咒的翻译方案", Content: "策略内容待填充"},

		// M-T01 信任建立 (T-001...T-007)
		{ID: "T-001", MetaID: "M-T01", Name: "视觉饱和攻击", Description: "3-5组案例预告-轰炸-封锁三连击", Content: "策略内容待填充"},
		{ID: "T-002", MetaID: "M-T01", Name: "社会证明嫁接", Description: "质疑转为100+同行案例库的群体智慧", Content: "策略内容待填充"},
		{ID: "T-003", MetaID: "M-T01", Name: "职业镜像防御", Description: "以未来同样保护你为由拒绝边界试探", Content: "策略内容待填充"},
		{ID: "T-004", MetaID: "M-T01", Name: "极速闭环碾压", Description: "认错并秒级提供3种替代方案", Content: "策略内容待填充"},
		{ID: "T-005", MetaID: "M-T01", Name: "情感账户充值", Description: "告知稀缺性理由+超额补偿", Content: "策略内容待填充"},
		{ID: "T-006", MetaID: "M-T01", Name: "焦虑植入与认知粉碎", Description: "指出低水平勤奋是毒药", Content: "策略内容待填充"},
		{ID: "T-007", MetaID: "M-T01", Name: "案例碾压与借口粉碎", Description: "效率案例粉碎借口", Content: "策略内容待填充"},

		// M-P02 专业边界 (P-001)
		{ID: "P-001", MetaID: "M-P02", Name: "交付边界与位势重构", Description: "情绪认同 -> 高位拒绝 -> 掌控节奏", Content: strategyContentP001},
	}
}

// P-001 完整策略内容 (保持不变)
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

// LoadStrategyContentsFromDir 从指定目录加载基础策略内容
// 目录结构要求：文件名必须为 {StrategyID}.txt 或 {StrategyID}.md
func LoadStrategyContentsFromDir(dirPath string, basics []domain.BasicStrategy) {
	for i := range basics {
		id := basics[i].ID
		// 尝试读取 txt
		content, err := os.ReadFile(filepath.Join(dirPath, id+".txt"))
		if err == nil {
			basics[i].Content = string(content)
			continue
		}
		// 尝试读取 md
		content, err = os.ReadFile(filepath.Join(dirPath, id+".md"))
		if err == nil {
			basics[i].Content = string(content)
			continue
		}
	}
}
