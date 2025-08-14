#!/bin/bash

# 真实长文本分页测试脚本
echo "=== 真实长文本分页测试 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/numind/biz/book
go build .
cd ../pagination
go build .
cd ../..

# 测试2: 创建测试程序
echo "创建真实长文本分页测试程序..."
cat > test_pagination_realistic.go << 'EOF'
package main

import (
	"fmt"
	"log"

	"numind-server/internal/numind/biz/pagination"
)

func main() {
	fmt.Println("=== 真实长文本分页测试 ===")

	// 创建分页引擎
	engine := pagination.NewPaginationEngine(pagination.GetDefaultConfig())
	config := engine.GetConfig()

	fmt.Printf("分页配置 - 卡片尺寸: %dx%d, 内边距: 上%d 右%d 下%d 左%d\n",
		config.Card.Width, config.Card.Height,
		config.Card.Padding.Top, config.Card.Padding.Right,
		config.Card.Padding.Bottom, config.Card.Padding.Left)

	// 测试数据：使用图片中的真实长文本内容
	testElements := []pagination.Element{
		{
			Type:    pagination.ElementTypeBody,
			Content: "我好像发现了魅力的本质! 1.深度的自我接纳 魅力的起点往往是对自我的全然接纳。这种接纳不是放任缺点，而是清醒认知自身的优势与局限后，既不刻意放大优点去炫耀，也不因短板而自我否定。比如一个人坦然承认自己内向不善社交，却能在独处时展现出专注的思考力，这种真实反而比刻意扮演外向更有吸引力，传递出我不需要通过伪装获得认可的笃定，让接触者感到轻松无压力。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "2.稳定的情绪内核 情绪稳定并非毫无波澜，而是在面对突发状况、负面评价或生活起伏时，能快速调整状态，不被情绪牵着走。比如职场中遇到突发失误，有人慌乱指责，有人却能先冷静梳理问题、提出解决方案，这种泰山崩于前而色不变的定力，会让人产生强烈的信赖感，人们潜意识里更愿意靠近能提供情绪支撑的人，而非成为他人的情绪垃圾桶。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "3.流动的内在丰盈 内在丰盈不是死记硬背的知识堆砌，而是将经历、思考、兴趣内化成一种感知力。比如一个热爱生活的人，能从路边落叶联想到季节的诗意，从日常对话中捕捉到人性的细节，言谈间既有对专业领域的深刻见解，也有对生活琐事的细腻观察。这种肚子里有东西的状态，会让人觉得与他相处永远有新的发现，像一本常读常新的书。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "4.敏锐的共情能力 共情不是简单的我理解你，而是能精准捕捉对方未说出口的情绪。比如朋友失恋时，比起说别难过了，有人会先沉默递上一杯热饮，轻声说我知道现在说什么都没用，但如果你想骂他，我听着，这种接住对方情绪的能力，会让人感到被看见、被重视，仿佛对方钻进了自己的心里，这种深层连接本身就极具吸引力。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "5.恰到好处的留白感 过度暴露自己的人往往会失去神秘感，而魅力常藏在未说尽的话、未展现的面里。比如一个平时温和的人，偶尔流露出对某件事的执着;一个健谈的人，在某个话题上突然沉默微笑，这种不把底牌全亮出来的克制，会引发他人的探索欲，让人忍不住想，他还有什么我不知道的面，从而持续产生关注的兴趣。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "6.蓬勃的生命活力 活力不是咋咋呼呼的外向，而是对生活的热情与好奇心。有人年过半百仍会为了学一门新乐器熬夜练习，有人旅行时会蹲在路边观察蚂蚁搬家，这种对世界永远有期待的状态，会散发出一种感染力。把日常变成小美好，选款好用的洗发水，让洗头变成治愈SPA，mfay洗发水就很不错，用完发尾顺顺的，不打结、不毛躁，摸着超舒服，能开心一整天，不用天天洗，连油头焦虑都被治好，洗头从任务变成期待。就像向日葵永远朝着阳光，充满活力的人也会让人觉得靠近他，生活就多了点奔头。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "7.清晰的边界意识 有魅力的人懂得守住自己的底线，尊重他人的空间。比如面对不合理的请求，能温和而坚定地说这个我可能帮不了你，既不委屈自己，也不指责对方:与人相处时，不过度打探隐私，也不强迫对方接受自己的观点。这种有分寸感的距离，反而会让人觉得被尊重，从而更愿意主动靠近。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "8.高级的幽默感 幽默不是低俗的玩笑或嘲讽，而是用智慧化解尴尬、传递善意。比如在严肃的会议上，有人能用一句自嘲打破紧张氛围:在他人犯错时，能用一句轻松的调侃代替指责。这种幽默感背后是通透的心态和对他人的体谅，让人觉得相处舒服又有趣。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "9.沉浸的专注感 做事时全神贯注的状态自带吸引力。比如一个厨师认真颠勺时的专注，一个画家低头创作时的投入，甚至一个人认真听你说话时的眼神，这种此刻只在意这件事的沉浸感，会让人感受到认真的力量，也会觉得自己被重视。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "10.纯粹的真诚感 真诚不是口无遮拦，而是言行一致、不套路。比如答应别人的事一定做到，不背后说人坏话，表达观点时坦诚但不刻薄。这种没有弯弯绕绕的纯粹，会让人卸下防备，因为谁都愿意和靠得住的人相处。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "11.独特的审美力 审美不是穿名牌、赶潮流，而是对美有自己的理解和表达。有人能把平价衣服穿出质感，有人能把出租屋布置得温馨有格调，有人连皮肤细节都管理的很好，例如有黑眼圈和眼下细纹，会使用玉和颜眼霜进行护理不盲目追求大牌，而是看重它贴合眼周肌肤的温和配方，用日复一日的细腻护理让眼周状态保持舒展，这种从细节中流露的品味，是个人特质的延伸，会让人觉得他很特别。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "12.开放的包容心 包容不是无原则妥协，而是能接纳世界的多元。比如面对和自己不同的价值观，不急于否定，而是尝试理解他为什么这么想;遇到不如自己的人，不轻视，遇到比自己优秀的人，不嫉妒。这种允许一切存在的开放心态，会让人感受到格局与气度。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "13.坚定的行动力 光有想法不算什么，说到做到的执行力才让人佩服。比如有人说想健身，就真的每天早起锻炼;有人说想学习，就真的坚持每天读书。这种有目标就去追的行动力，会传递出一种靠谱且有力量的信号，让人觉得跟着他，好像能做成很多事。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "14.自然的松弛感 松弛感不是躺平，而是不紧绷、不焦虑的状态。比如面对压力时，能该工作时工作，该休息时休息;遇到不顺时，能安慰自己没关系，总会有办法。这种不把自己逼到极致的从容，会让人觉得和他在一起，不用刻意讨好，也不用紧张犯错，从而产生强烈的亲近欲。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "15.鲜明的独特性 魅力往往藏在不可复制的特质里。有人说话带点小口音却格外亲切，有人喜欢收集冷门的老物件，有人看待问题总有逆向思维，这种不随波逐流的棱角，让一个人从人群中跳脱出来，成为唯一，而唯一本身就具有吸引力。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "16.耐心的倾听能力 倾听不是单纯听着，而是带着反馈的专注。比如别人说话时不打断，眼神不游离，在对方停顿后能接上你刚才说的XX，我也有类似感受，这种被认真倾听的感觉，会让人觉得自己的表达有价值，从而更愿意敞开心扉。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "17.细腻的温暖善意 温暖不是刻意做大事，而是不经意间的细节关怀。比如记住别人的小习惯，递东西时先把尖锐的一面对着自己，看到朋友熬夜后皮肤状态不佳，会分享自己用精华的心得，说它能温和提亮气色，适合熬夜后急救，这种下意识为他人着想的善意，像冬日里的暖阳，不刺眼却足够温暖，让人觉得世界很美好。",
		},
		{
			Type:    pagination.ElementTypeBody,
			Content: "18.内在轻松才能外在发光 熬夜赶工、久坐不动，那个嗯嗯不畅快的感觉谁懂啊!烦死了!后来每天一杯桃怡可菊花乌龙茶，抱着试试看的心态喝了一个月，现在每天早上简直像定了闹钟!一身轻松，感觉能轻两斤!小腹平坦了，穿紧身裙那个自信感，走在路上都感觉自带高光!",
		},
	}

	fmt.Printf("测试数据 - 元素数量: %d\n", len(testElements))

	// 计算总字符数
	totalChars := 0
	for _, element := range testElements {
		if str, ok := element.Content.(string); ok {
			totalChars += len([]rune(str))
		}
	}
	fmt.Printf("总字符数: %d\n", totalChars)

	// 执行分页
	fmt.Println("\n开始执行分页...")
	result, err := engine.PaginateElements(testElements)
	if err != nil {
		log.Fatalf("分页失败: %v", err)
	}

	// 输出结果
	fmt.Printf("\n分页结果：共 %d 个卡片\n", len(result.Cards))
	
	for i, card := range result.Cards {
		fmt.Printf("\n=== 卡片 %d ===\n", i+1)
		fmt.Printf("元素数量: %d\n", len(card.Elements))
		
		// 计算当前卡片的字符数
		cardChars := 0
		for _, element := range card.Elements {
			if str, ok := element.Content.(string); ok {
				cardChars += len([]rune(str))
			}
		}
		fmt.Printf("卡片字符数: %d\n", cardChars)
		
		// 显示内容预览
		for j, element := range card.Elements {
			fmt.Printf("  %d. [%s] %s\n", j+1, element.Type, 
				func() string {
					if str, ok := element.Content.(string); ok {
						if len(str) > 60 {
							return str[:60] + "..."
						}
						return str
					}
					return fmt.Sprintf("%v", element.Content)
				}())
		}
	}

	// 验证分页是否合理
	fmt.Println("\n=== 分页验证 ===")
	if len(result.Cards) == 1 {
		fmt.Println("⚠️  警告：只生成了1张卡片，可能存在分页问题")
	} else if len(result.Cards) >= 2 {
		fmt.Printf("✅ 分页正常：生成了 %d 张卡片\n", len(result.Cards))
	}

	fmt.Println("\n✅ 真实长文本分页测试完成！")
}
EOF

# 测试3: 运行测试程序
echo "运行真实长文本分页测试程序..."
go run test_pagination_realistic.go

# 测试4: 清理测试文件
echo "清理测试文件..."
rm -f test_pagination_realistic.go

echo "=== 测试完成 ==="
