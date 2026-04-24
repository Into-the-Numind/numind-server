package sop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/tokenizer"
)

// testTokenizer 是所有 trim 测试共享的实例（tiktoken 首次初始化有几秒开销）
var testTokenizer *tokenizer.Tokenizer
var testTokenizerOnce sync.Once

func TestMain(m *testing.M) {
	testTokenizerOnce.Do(func() {
		tk, err := tokenizer.NewTokenizer()
		if err != nil {
			panic(err)
		}
		testTokenizer = tk
	})
	os.Exit(m.Run())
}

func newExecForTrimTest(t *testing.T) *SopExecutor {
	t.Helper()
	require.NotNil(t, testTokenizer, "testTokenizer must be initialized via TestMain")
	return &SopExecutor{tokenizer: testTokenizer}
}

// bigCJK 产生 n 个汉字的字符串（每个 3 字节 UTF-8），cl100k_base 下约 1.5-2 tokens/字。
// 使用多样化的词组避免触发 tiktoken BPE 病态长合并（prod 真实数据也是多样化的）。
func bigCJK(n int) string {
	parts := []string{"产品介绍", "市场分析", "客户反馈", "销售策略", "运营方案", "内容概要", "数据报告", "业务目标"}
	var b strings.Builder
	for b.Len() < n*3 { // 预分配足够字节（每汉字 3 字节）
		for _, p := range parts {
			b.WriteString(p)
			b.WriteString("，")
			if b.Len() >= n*3 {
				break
			}
		}
	}
	// 按 rune 精确截到 n 个字符
	runes := []rune(b.String())
	if len(runes) > n {
		runes = runes[:n]
	}
	return string(runes)
}

// ---- stripAttachmentBlocks 单元测试 ----

func TestStripAttachmentBlocks_SingleBlock(t *testing.T) {
	input := "用户说明\n\n=== report.docx ===\n这是报告内容\n很多行\n更多内容"
	got, files, saved := stripAttachmentBlocks(input)
	assert.Equal(t, []string{"report.docx"}, files)
	assert.Contains(t, got, "[附件已省略: report.docx]")
	assert.NotContains(t, got, "这是报告内容")
	assert.Contains(t, got, "用户说明")
	assert.Greater(t, saved, 0)
}

func TestStripAttachmentBlocks_MultipleBlocks(t *testing.T) {
	input := "用户文字\n\n=== a.docx ===\n内容A\n\n=== b.pdf ===\n内容B"
	got, files, _ := stripAttachmentBlocks(input)
	assert.ElementsMatch(t, []string{"a.docx", "b.pdf"}, files)
	assert.Contains(t, got, "[附件已省略: a.docx]")
	assert.Contains(t, got, "[附件已省略: b.pdf]")
	assert.NotContains(t, got, "内容A")
	assert.NotContains(t, got, "内容B")
	assert.Contains(t, got, "用户文字")
}

func TestStripAttachmentBlocks_NoExtensionMatch(t *testing.T) {
	// 用户正文里恰好有 === xxx === 格式但无文件扩展名 → 不应误伤
	input := "=== 我的笔记 ===\n一些内容"
	got, files, saved := stripAttachmentBlocks(input)
	assert.Empty(t, files)
	assert.Equal(t, input, got)
	assert.Equal(t, 0, saved)
}

func TestStripAttachmentBlocks_NoAttachments(t *testing.T) {
	input := "纯文本，无任何附件"
	got, files, saved := stripAttachmentBlocks(input)
	assert.Empty(t, files)
	assert.Equal(t, input, got)
	assert.Equal(t, 0, saved)
}

func TestStripAttachmentBlocks_FilenameWithDots(t *testing.T) {
	// 文件名含中间的点（file.v2.docx）
	input := "=== file.v2.docx ===\ncontent"
	got, files, _ := stripAttachmentBlocks(input)
	assert.Equal(t, []string{"file.v2.docx"}, files)
	assert.Contains(t, got, "[附件已省略: file.v2.docx]")
}

// ---- trimHistoryForGateway 行为测试 ----

func TestTrimHistoryForGateway_UnderCap_NoChange(t *testing.T) {
	e := newExecForTrimTest(t)
	msgs := []LLMMessage{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "问题一"},
		{Role: "assistant", Content: "回答一"},
		{Role: "user", Content: "当前问题"},
	}
	got, err := e.trimHistoryForGateway(context.Background(), msgs)
	require.NoError(t, err)
	assert.Equal(t, msgs, got)
}

func TestTrimHistoryForGateway_StripOldestAttachmentFirst(t *testing.T) {
	e := newExecForTrimTest(t)
	// 两个历史节点都带附件。big 足够大以保证触发裁剪：100k 汉字 ≈ 150k+ cl100k tokens。
	big := bigCJK(100000)
	msgs := []LLMMessage{
		{Role: "system", Content: "系统提示"},
		{Role: "user", Content: fmt.Sprintf("节点一输入\n\n=== big.docx ===\n%s", big)},
		{Role: "assistant", Content: "节点一输出总结"},
		{Role: "user", Content: "节点二输入\n\n=== small.pdf ===\n小附件内容"},
		{Role: "assistant", Content: "节点二输出"},
		{Role: "user", Content: "当前问题"},
	}

	got, err := e.trimHistoryForGateway(context.Background(), msgs)
	require.NoError(t, err)

	// big.docx 必须被剥（它是最早的且最大的）
	assert.Contains(t, got[1].Content, "[附件已省略: big.docx]", "oldest attachment should be stripped first")
	assert.NotContains(t, got[1].Content, big[:300])
	// 节点一的正文保留
	assert.Contains(t, got[1].Content, "节点一输入")
	// 当前步骤不动
	assert.Equal(t, "当前问题", got[len(got)-1].Content)
	// system 不动
	assert.Equal(t, "系统提示", got[0].Content)
	// 总 tokens 在 cap 以下
	assert.LessOrEqual(t, estimateExactTokens(e.tokenizer, got), gatewayTokenCap)
}

func TestTrimHistoryForGateway_CurrentStepAttachmentPreserved(t *testing.T) {
	e := newExecForTrimTest(t)
	big := bigCJK(100000)
	msgs := []LLMMessage{
		{Role: "system", Content: "系统提示"},
		{Role: "user", Content: fmt.Sprintf("=== history.docx ===\n%s", big)},
		{Role: "assistant", Content: "历史输出"},
		{Role: "user", Content: "当前问题\n\n=== current.docx ===\n当前步骤的附件，不能动"},
	}

	got, err := e.trimHistoryForGateway(context.Background(), msgs)
	require.NoError(t, err)
	// 历史附件被剥
	assert.Contains(t, got[1].Content, "[附件已省略: history.docx]")
	// 当前步骤附件完整保留
	assert.Contains(t, got[len(got)-1].Content, "=== current.docx ===")
	assert.Contains(t, got[len(got)-1].Content, "当前步骤的附件，不能动")
}

func TestTrimHistoryForGateway_FallthroughDropOldestMessagePair(t *testing.T) {
	e := newExecForTrimTest(t)
	// Vicky 场景：历史步骤是超长粘贴文本（无附件 marker），剥附件一轮 0 节省 → 必须 fallthrough 到丢整段
	big := bigCJK(100000)
	msgs := []LLMMessage{
		{Role: "system", Content: "系统"},
		{Role: "user", Content: "一" + big},
		{Role: "assistant", Content: "输出一"},
		{Role: "user", Content: "二"},
		{Role: "assistant", Content: "输出二"},
		{Role: "user", Content: "当前"},
	}

	got, err := e.trimHistoryForGateway(context.Background(), msgs)
	require.NoError(t, err)

	// 最早的 (user 一 + assistant 输出一) 应被整段丢
	for _, m := range got {
		assert.False(t, strings.HasPrefix(m.Content, "一"), "最早历史 user 应被丢弃")
		assert.NotEqual(t, "输出一", m.Content, "其对应 assistant 也应被丢弃")
	}
	// 当前和 system 保留
	assert.Equal(t, "当前", got[len(got)-1].Content)
	assert.Equal(t, "系统", got[0].Content)
	assert.LessOrEqual(t, estimateExactTokens(e.tokenizer, got), gatewayTokenCap)
}

func TestTrimHistoryForGateway_CurrentTooLongReturnsError(t *testing.T) {
	e := newExecForTrimTest(t)
	// 单条 user 就超 cap：100k 汉字 ≈ 150k+ tokens > 85k
	huge := bigCJK(100000)
	msgs := []LLMMessage{
		{Role: "system", Content: "系统"},
		{Role: "user", Content: huge},
	}

	got, err := e.trimHistoryForGateway(context.Background(), msgs)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGatewayInputTooLong))
	// 当前步骤内容不被截断
	assert.Equal(t, huge, got[len(got)-1].Content)
}

func TestTrimHistoryForGateway_NilTokenizer_NoChange(t *testing.T) {
	// tokenizer 初始化失败时（SopExecutor.tokenizer == nil）应优雅降级，不动消息
	e := &SopExecutor{tokenizer: nil}
	msgs := []LLMMessage{
		{Role: "user", Content: bigCJK(10000)},
	}
	got, err := e.trimHistoryForGateway(context.Background(), msgs)
	require.NoError(t, err)
	assert.Equal(t, msgs, got)
}

func TestTrimHistoryForGateway_VickyRealisticScenario(t *testing.T) {
	// 模拟 Vicky prod run 1592 节点 8 的真实场景：
	// system prompt + node5 user (160k 字符，粘贴无附件) + node5 output +
	// node6 user/output + node7 user/output + node8 current input (含附件)
	// 原始场景：总量 ~98k+ 真实 tokens，上游 98304 上限触发 HTTP 400
	e := newExecForTrimTest(t)
	msgs := []LLMMessage{
		{Role: "system", Content: bigCJK(1000)},
		{Role: "user", Content: bigCJK(160000)}, // 粘贴无附件的大段文本
		{Role: "assistant", Content: bigCJK(2400)},
		{Role: "user", Content: bigCJK(6000)},
		{Role: "assistant", Content: bigCJK(1600)},
		{Role: "user", Content: bigCJK(600)},
		{Role: "assistant", Content: bigCJK(400)},
		{Role: "user", Content: "当前仿写任务\n\n=== transcript.docx ===\n" + bigCJK(32000)},
	}

	got, err := e.trimHistoryForGateway(context.Background(), msgs)
	require.NoError(t, err, "Vicky 场景必须能裁剪成功")

	// 总量 ≤ cap
	total := estimateExactTokens(e.tokenizer, got)
	assert.LessOrEqual(t, total, gatewayTokenCap, "裁剪后必须 ≤ %d, 实际 %d", gatewayTokenCap, total)

	// 当前步骤（含附件）完整保留
	assert.Contains(t, got[len(got)-1].Content, "=== transcript.docx ===")
	assert.Contains(t, got[len(got)-1].Content, "当前仿写任务")
	assert.Equal(t, msgs[len(msgs)-1].Content, got[len(got)-1].Content)
	// system 保留
	assert.Equal(t, msgs[0].Content, got[0].Content)
}
