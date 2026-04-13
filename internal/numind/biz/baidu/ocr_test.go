package baidu

import (
	"strings"
	"testing"
)

func TestIsWechatTimestamp(t *testing.T) {
	shouldMatch := []string{
		"14:30",
		"9:00",
		"下午 2:30",
		"上午 9:00",
		"凌晨 3:15",
		"中午 12:00",
		"晚上 8:30",
		"昨天 下午 3:45",
		"前天 上午 10:00",
		"今天 14:30",
		"星期三 上午 9:00",
		"星期日 下午 2:30",
		"星期天 8:00",
		"10月15日 下午 2:30",
		"3月5日 9:00",
		"10月15日 星期三 上午 9:00",
		"2024年10月15日 下午 2:30",
		"2024年3月5日 上午 11:00",
	}
	for _, s := range shouldMatch {
		if !isWechatTimestamp(s) {
			t.Errorf("expected match: %q", s)
		}
	}

	shouldNotMatch := []string{
		"3:00可以吗",
		"下午见",
		"明天下午2点",
		"你好",
		"10月15日开会",
		"昨天去了商场",
		"星期三有空吗",
		"",
		"价格是3:1的比例",
	}
	for _, s := range shouldNotMatch {
		if isWechatTimestamp(s) {
			t.Errorf("unexpected match: %q", s)
		}
	}
}

func TestIsVoiceDuration(t *testing.T) {
	shouldMatch := []string{
		`5"`, `15"`, `60"`, `120"`,
		"5''", "15''",
		"5″",
		"1'23\"", "2'00",
	}
	for _, s := range shouldMatch {
		if !isVoiceDuration(s) {
			t.Errorf("expected match: %q", s)
		}
	}

	shouldNotMatch := []string{
		"你好",
		"5块钱",
		"15",    // 纯数字不应匹配
		"价格5\"", // 非独立时长
		"3:00可以吗",
	}
	for _, s := range shouldNotMatch {
		if isVoiceDuration(s) {
			t.Errorf("unexpected match: %q", s)
		}
	}
}

func TestIsSystemMessage(t *testing.T) {
	imgWidth := 1080

	shouldFilter := []struct {
		text    string
		centerX float64
	}{
		{"你已添加了张三为好友", 540},
		{"张三撤回了一条消息", 540},
		{"张三拍了拍你", 500},
		{"以下是新消息", 540},
		{"消息已发出，但被对方拒收了", 540},
		{"张三发起了语音通话", 540},
	}
	for _, tc := range shouldFilter {
		if !isSystemMessage(tc.text, tc.centerX, imgWidth) {
			t.Errorf("expected system message: %q centerX=%.0f", tc.text, tc.centerX)
		}
	}

	shouldKeep := []struct {
		text    string
		centerX float64
	}{
		{"我已添加了他的微信", 150},
		{"消息已发出去了哦", 900},
		{"这是一条超长的系统通知消息，已添加了非常多的内容在里面，超过了三十个字符的限制", 540},
		{"你好，请问一下", 540},
	}
	for _, tc := range shouldKeep {
		if isSystemMessage(tc.text, tc.centerX, imgWidth) {
			t.Errorf("unexpected system message: %q centerX=%.0f", tc.text, tc.centerX)
		}
	}
}

func TestFormatChatMessages(t *testing.T) {
	imgWidth := 1080

	items := []WordsItem{
		// 时间分隔符（应被过滤）
		{Words: "下午 2:30", Location: Location{Left: 450, Top: 100, Width: 180, Height: 30}},
		// 左侧消息（两行，同一气泡）
		{Words: "你好，请问一下", Location: Location{Left: 120, Top: 200, Width: 250, Height: 35}},
		{Words: "这个产品怎么样", Location: Location{Left: 120, Top: 240, Width: 250, Height: 35}},
		// 右侧消息（三行，同一气泡）
		{Words: "你好！这款产品非常适合您的需求", Location: Location{Left: 350, Top: 350, Width: 600, Height: 35}},
		{Words: "目前正在做促销活动", Location: Location{Left: 350, Top: 390, Width: 400, Height: 35}},
		{Words: "价格很优惠", Location: Location{Left: 350, Top: 430, Width: 200, Height: 35}},
		// 系统通知（应被过滤）
		{Words: "张三拍了拍你", Location: Location{Left: 400, Top: 500, Width: 200, Height: 30}},
		// 独立消息
		{Words: "多少钱", Location: Location{Left: 120, Top: 650, Width: 100, Height: 35}},
	}

	result := formatChatMessages(items, imgWidth)

	// 无说话人标注，同一气泡多行合并，不同气泡换行
	expected := "你好，请问一下这个产品怎么样\n你好！这款产品非常适合您的需求目前正在做促销活动价格很优惠\n多少钱"
	if result != expected {
		t.Errorf("formatChatMessages result mismatch\ngot:  %q\nwant: %q", result, expected)
	}
}

func TestFormatChatMessages_NoiseFiltering(t *testing.T) {
	imgWidth := 750

	items := []WordsItem{
		// UI 元素（应被过滤）
		{Words: "<", Location: Location{Left: 20, Top: 20, Width: 20, Height: 30}},
		{Words: "···", Location: Location{Left: 700, Top: 20, Width: 30, Height: 30}},
		// 聊天内容
		{Words: "很近过去", Location: Location{Left: 110, Top: 220, Width: 130, Height: 35}},
		{Words: "游刃有余", Location: Location{Left: 110, Top: 310, Width: 130, Height: 35}},
		// 时间分隔（应被过滤）
		{Words: "上午 11:38", Location: Location{Left: 310, Top: 850, Width: 130, Height: 25}},
		// 消息
		{Words: "今天空了顺带帮我看看付款", Location: Location{Left: 250, Top: 940, Width: 400, Height: 35}},
		// 底部 UI（应被过滤）
		{Words: "+", Location: Location{Left: 710, Top: 1250, Width: 25, Height: 25}},
	}

	result := formatChatMessages(items, imgWidth)

	if strings.Contains(result, "+") {
		t.Errorf("UI '+' should be filtered, got: %s", result)
	}
	if strings.Contains(result, "11:38") {
		t.Errorf("timestamp should be filtered, got: %s", result)
	}
	if !strings.Contains(result, "很近过去") {
		t.Errorf("missing '很近过去', got: %s", result)
	}
	if !strings.Contains(result, "今天空了顺带帮我看看付款") {
		t.Errorf("missing message, got: %s", result)
	}

	t.Logf("Result:\n%s", result)
}

func TestIsUIElement(t *testing.T) {
	imgWidth := 750

	shouldFilter := []struct {
		text string
		loc  Location
	}{
		{"+", Location{Left: 710, Top: 1250, Width: 25, Height: 25}},
		{"<", Location{Left: 20, Top: 20, Width: 20, Height: 30}},
		{">", Location{Left: 720, Top: 300, Width: 15, Height: 25}},
		{"···", Location{Left: 700, Top: 20, Width: 30, Height: 30}},
		{"1", Location{Left: 50, Top: 20, Width: 20, Height: 30}},
		{"返回", Location{Left: 690, Top: 20, Width: 50, Height: 25}},
	}
	for _, tc := range shouldFilter {
		if !isUIElement(tc.text, tc.loc, imgWidth) {
			t.Errorf("expected UI element: %q at Left=%d", tc.text, tc.loc.Left)
		}
	}

	shouldKeep := []struct {
		text string
		loc  Location
	}{
		{"你好", Location{Left: 120, Top: 200, Width: 80, Height: 35}},
		{"好", Location{Left: 120, Top: 400, Width: 50, Height: 35}},
		{"OK", Location{Left: 500, Top: 300, Width: 60, Height: 35}},
	}
	for _, tc := range shouldKeep {
		if isUIElement(tc.text, tc.loc, imgWidth) {
			t.Errorf("unexpected UI element: %q at Left=%d", tc.text, tc.loc.Left)
		}
	}
}

func TestFormatChatMessages_MultipleMessages(t *testing.T) {
	imgWidth := 1080

	// 两条独立消息（间距大，应保持独立行）
	items := []WordsItem{
		{Words: "你好", Location: Location{Left: 120, Top: 100, Width: 80, Height: 35}},
		// 间距大（Top=300 vs 上一条 Bottom=135）
		{Words: "在吗", Location: Location{Left: 120, Top: 300, Width: 80, Height: 35}},
	}

	result := formatChatMessages(items, imgWidth)
	expected := "你好\n在吗"
	if result != expected {
		t.Errorf("multi-msg mismatch\ngot:  %q\nwant: %q", result, expected)
	}
}
