package skill

// QuestionnaireAnswers 保存问卷 Q6-Q12 的答案快照。
// 所有字段使用 omitempty，允许旧快照 unmarshal 时缺少字段（schema 演进向后兼容）。
// 解析时禁用 DisallowUnknownFields，允许未来新增字段而旧代码不报错。
//
// Q1/Q3/Q4/Q5 存储在 AgentDefinition 直接字段（name/description/welcome_message/starters）。
// Q2 对应 icon_url 字段。
type QuestionnaireAnswers struct {
	Q6  []string `json:"q6,omitempty"`  // 任务类型多选 必填
	Q7  []string `json:"q7,omitempty"`  // 材料类型多选 必填
	Q8  int      `json:"q8,omitempty"`  // 积分上限 200-2000；0 视为 default 800
	Q9  string   `json:"q9,omitempty"`  // 网络搜索 radio "no_web_search" | "allow_search"
	Q10 string   `json:"q10,omitempty"` // 注意话题 可选
	Q11 string   `json:"q11,omitempty"` // 超范围话术 可选
	Q12 string   `json:"q12,omitempty"` // 说话风格 "friendly" | "professional" | "encouraging" 必填
}

// taskTypeDisplay 把 Q6 任务类型代码映射为中文 prompt 用语。
func taskTypeDisplay(t string) string {
	switch t {
	case "analyze_data":
		return "分析数据 / 报表"
	case "generate_content":
		return "生成文字内容"
	case "answer_questions":
		return "回答问题 / 答疑"
	case "make_plan":
		return "帮助制定计划"
	case "grade_assignment":
		return "批改 / 评分学员作业"
	default:
		return t
	}
}

// materialTypeDisplay 把 Q7 材料类型代码映射为中文 prompt 用语。
func materialTypeDisplay(m string) string {
	switch m {
	case "text":
		return "文字（笔记、日报、复盘）"
	case "csv":
		return "Excel / CSV 数据表格"
	case "image":
		return "图片（截图、海报）"
	case "none":
		return "不需要上传"
	default:
		return m
	}
}

// styleDisplay 把 Q12 说话风格代码映射为中文 prompt 用语。
func styleDisplay(s string) string {
	switch s {
	case "friendly":
		return "亲切活泼的风格"
	case "professional":
		return "专业严谨的风格"
	case "encouraging":
		return "鼓励陪伴的风格"
	default:
		return s
	}
}
