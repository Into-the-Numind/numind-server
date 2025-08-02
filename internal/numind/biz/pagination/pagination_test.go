package pagination

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestPaginationEngine(t *testing.T) {
	// 测试数据
	elements := []Element{
		{
			Type:    ElementTypeTitle,
			Content: "为什么高价值的信息几乎从不流向普通人？",
		},
		{
			Type: ElementTypeBody,
			Content: "因为流不动，容易被误解，甚至被"拒收"。价值越高的东西，越考验人的理解能力。如果理解不了，就等于"接收不到"。记得几年前，知识付费行业刚开始蓄势的时候，一个好友劝我说："你知道吗？那个《灵魂有香气的女子》的作者，就靠卖女性成长类的内容，卖爆了。你要不也去做知识付费吧，未来肯定很火。"因为我一直在影视行业工作，隔行如隔山，随口问了句，"什么是知识付费？"他当时就无语了。",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "高深内容必须在简化之后，才能被普通人接受",
		},
		{
			Type: ElementTypeBody,
			Content: "因为只有简单，才容易被"传承"，但同时，包着简单外壳的复杂信息，也很容易被误解。比如很多经典都是用文言文写的，但老百姓更倾向于传奇故事、谚语、俗语。",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "高价值的信息可能会被"拒收"，也是因为"被误解"得太离谱",
		},
		{
			Type: ElementTypeList,
			Content: []string{
				"《道德经》中的"无为"被解读成"什么都不做"，而不是"顺应自然而为"。",
				""以德报怨"，被误解为"被人欺负要用爱心感化"。但原文是："以德报怨，何以报德？以直报怨，以德报德。"，孔子表达的是反对无原则宽容，主张公正回应。",
				""人不为己，天诛地灭"被误解为"人人都应该自私自利"，但"为"读wéi，指的是"修为"，不修身则天地不容。",
			},
		},
		{
			Type: ElementTypeBody,
			Content: "还有些人思想跟不上时代，认为"不上班赚钱=不正经"、"离婚=没体面"、"鼓励女性独立=破坏家庭稳定"……等等。本来一个正向的东西，被人理解为一个负面的东西，所以遭到了排斥。",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "普通人不太喜欢深究，只关注皮毛",
		},
		{
			Type: ElementTypeBody,
			Content: "就比如现在互联网上的一些"吸引人的猎奇事件"，大多数人都在讨论"怎么会？"，几乎很少有人关注"为什么？"。有时候"不求甚解"并非好事。这也是我开始正经去学习《大学》《论语》之后，才明白的一点：我们曾经学到的东西，都不是正确的，也不是不正确，而是太表面。",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "举个简单的例子",
		},
		{
			Type:    ElementTypeQuote,
			Content: "《论语》中说：有朋自远方来，不亦乐乎？",
		},
		{
			Type: ElementTypeBody,
			Content: "这句话，大部分人会翻译为：朋友从远处来，我很高兴。严谨点的朋友，会翻译为：有志同道合的朋友从远方来，不也是很快乐的事吗？但认真研读古籍经典，要通过文字训诂还原古代文字的真实意义，结合时代背景和事件来判断，结合五经互证。",
		},
		{
			Type: ElementTypeList,
			Content: []string{
				""同门曰朋"，「朋」就是师门的同学。",
				"「说」代表个体性的情感，发自内心的高兴；而「乐」是一个人和周边环境的互动，在融洽协调中获得的一种体验。",
				"当时孔子的道德学问广传天下，全国各地的人都来学习。",
			},
		},
		{
			Type: ElementTypeBody,
			Content: "现在再来看这句话，是否有不同的感触呢？",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "真正有用的东西，都是枯燥乏味的，需要花很多时间和精力",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "高价值的信息在传播的过程中总是"被迫降级"",
		},
		{
			Type: ElementTypeBody,
			Content: "因为，认知门槛低的信息更容易理解；人们更愿意相信"简单答案"，而非复杂思考；太多人在靠简化信息来赚钱。总之，否认真理不仅可以掩盖自身的懒惰，甚至可以迎合大众，快速获利。这样一来的后果是：人们更愿意掩耳盗铃的相信"短平快的捷径"而非"踏实的付出与真实的行动"。那些名人学者总结出来的高价值信息，已经变成了人们为维护自己利益、为自己行为辩解的"有力武器"。事实到底是什么，好像已经不重要，能否受益也不重要，只有利益最重要，评判最重要。",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "比如，一个人说"这个东西有用"，另一个人说"骗人，根本没有用"",
		},
		{
			Type: ElementTypeBody,
			Content: "那么双方到底谁是对的？谁会胜出？可能这个东西真的有用，人家只是真诚分享；可能这东西确实没有用，就想骗人；可能说骗人的人，认知狭隘，为维护自己的立场辩驳；可能他们之间有恩怨，所以敌对；可能前者说了实话，触及了后者的利益……总之，我们无从得知。",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "优质信息从古代的"精准传播困难"，变成了现在的"罗生门"",
		},
		{
			Type: ElementTypeBody,
			Content: "普通人已经很难识别出什么是真正有价值的信息了。网络上的信息已被严重污染，逐渐失去价值。碎片化内容满天飞，多巴胺短视频更吸引人的注意，我们一直在被动接收"二手信息"、传递"三手信息"、最后这个信息会在传播中变形。我们都变成了"信息中介"，知识只是短暂的从大脑中走过，留不下一点痕迹。",
		},
		{
			Type:    ElementTypeSubtitle,
			Content: "真正有用的，是我们的行动",
		},
		{
			Type: ElementTypeList,
			Content: []string{
				"不要看信息，要去验证信息的真伪；",
				"不要轻易相信结论，要去实践得出结论的那件事；",
				"不要在自己的认知范围内犟，要永远打开一扇门给"未知"。",
			},
		},
	}

	// 创建分页引擎
	engine := NewPaginationEngine(GetDefaultConfig())

	// 执行分页
	result, err := engine.Paginate(elements)
	if err != nil {
		t.Fatalf("分页失败: %v", err)
	}

	// 输出结果
	fmt.Printf("分页结果：共 %d 个卡片\n", len(result.Cards))
	for i, card := range result.Cards {
		fmt.Printf("\n=== 卡片 %d ===\n", i+1)
		for j, element := range card.Elements {
			fmt.Printf("%d. [%s] %v\n", j+1, element.Type, element.Content)
		}
	}

	// 验证结果
	if len(result.Cards) == 0 {
		t.Error("分页结果为空")
	}

	// 将结果转换为JSON并打印
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatalf("JSON序列化失败: %v", err)
	}
	fmt.Printf("\n=== JSON 输出 ===\n%s\n", string(jsonData))
}

func TestPaginateFromJSON(t *testing.T) {
	// 测试JSON字符串
	jsonStr := `[
		{
			"type": "title",
			"content": "为什么高价值的信息几乎从不流向普通人？"
		},
		{
			"type": "body",
			"content": "因为流不动，容易被误解，甚至被"拒收"。价值越高的东西，越考验人的理解能力。"
		},
		{
			"type": "list",
			"content": [
				"《道德经》中的"无为"被解读成"什么都不做"。",
				""以德报怨"，被误解为"被人欺负要用爱心感化"。"
			]
		}
	]`

	result, err := PaginateFromJSON(jsonStr)
	if err != nil {
		t.Fatalf("从JSON分页失败: %v", err)
	}

	fmt.Printf("JSON分页结果：共 %d 个卡片\n", len(result.Cards))
	for i, card := range result.Cards {
		fmt.Printf("\n=== 卡片 %d ===\n", i+1)
		for j, element := range card.Elements {
			fmt.Printf("%d. [%s] %v\n", j+1, element.Type, element.Content)
		}
	}
} 