package ingest

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/retrieval/domain"
)

// FlagDoc2Query 控制 ingest 期 doc2query 自动问题生成（RAG 升级项4，破情境教练类天花板）。
// 关（默认/缺省）→ 不生成，EmbedText 维持现状（结构感知面包屑+正文），prod 零变化。
const FlagDoc2Query = "features.doc2query.enabled"

const doc2queryPromptTmpl = `你是销售知识库的检索索引助手。下面是一段销售知识片段。请列出真实用户或销售人员**可能用来检索到这段内容的问题**，覆盖两类：①直接事实查询；②口语化/情境化表述（如客户异议的原话、销售遇到的真实场景）。每行一个问题，共 3-5 个，不要编号、不要解释、不要照抄原文。

知识片段：
%s

可能的提问：`

// Doc2QueryGenerator 在 ingest 时给每个 chunk 用 LLM 生成"它能回答哪些问题"，把这些问题
// 追加进 chunk.EmbedText（一起被向量化），桥接"情境提问 ↔ 答案片段"的字面鸿沟——情境
// 教练类问题（"客户老说再考虑怎么办"）与答它的案例（"预算犹豫分期促成"）几乎零字面重叠，
// 纯语义检索碰运气；生成的问题与真实提问同构 → 大幅提命中。Content 保持干净，问题只进 EmbedText。
type Doc2QueryGenerator struct {
	concurrency int
}

// NewDoc2QueryGenerator 创建生成器（并发 8）。
func NewDoc2QueryGenerator() *Doc2QueryGenerator {
	return &Doc2QueryGenerator{concurrency: 8}
}

// Enabled 读 feature flag。
func (g *Doc2QueryGenerator) Enabled() bool {
	return viper.GetBool(FlagDoc2Query)
}

// MaybeAugment：flag 开时为每个 chunk 生成问题并追加进 EmbedText（并行，best-effort：单块
// 失败只跳过不影响入库）。g==nil / flag 关 / 空输入 → no-op，零回归。
func (g *Doc2QueryGenerator) MaybeAugment(ctx context.Context, chunks []*domain.KnowledgeChunk) {
	if g == nil || !g.Enabled() || len(chunks) == 0 {
		return
	}
	// 内部调用：userID=0 + 跳过遗留计费 → doc2query 算公司成本，不扣用户积分（项4 计划）。
	ctx = aismw.WithUserID(ctx, 0)
	ctx = aiservice.WithSkipLegacyBilling(ctx)

	sem := make(chan struct{}, g.concurrency)
	var wg sync.WaitGroup
	var augmented int
	var mu sync.Mutex
	for _, c := range chunks {
		if c == nil || strings.TrimSpace(c.Content) == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(chunk *domain.KnowledgeChunk) {
			defer wg.Done()
			defer func() { <-sem }()
			qs := g.generate(ctx, chunk.Content)
			if len(qs) == 0 {
				return
			}
			base := chunk.EmbedText
			if base == "" {
				base = chunk.Content
			}
			chunk.EmbedText = base + "\n\n[可回答的问题]\n" + strings.Join(qs, "\n")
			mu.Lock()
			augmented++
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	log.Infow("doc2query augmentation done", "chunks", len(chunks), "augmented", augmented)
}

func (g *Doc2QueryGenerator) generate(ctx context.Context, content string) []string {
	prompt := fmt.Sprintf(doc2queryPromptTmpl, content)
	for i := 0; i < 2; i++ {
		resp, err := aiservice.Chat(ctx, profile.SalesragTagging, aiservice.ChatRequest{
			Messages:    []aiservice.ChatMessage{{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: prompt}}},
			Temperature: 0.3,
			MaxTokens:   256,
		})
		if err != nil {
			select {
			case <-time.After(400 * time.Millisecond):
			case <-ctx.Done():
				return nil // ctx 取消（删文档/超时）→ 立即放弃，不空等
			}
			continue
		}
		return parseDoc2QueryLines(resp.Content)
	}
	return nil
}

// doc2qPrefixRe 只剥"列表编号 + 分隔符"或"项目符号"前缀（如 "1." "2、" "3)" "- " "• "），
// 不用 TrimLeft cutset——否则会误吃数字开头的合法问题（"3年内能拿offer吗" → "年内…"）。
var doc2qPrefixRe = regexp.MustCompile(`^[\s　]*(?:\d{1,3}\s*[.、)）]|[-*·•＞>])\s*`)

// parseDoc2QueryLines 把 LLM 输出按行解析成问题列表：去编号/符号前缀、去重、过短丢弃、最多 5 条。
func parseDoc2QueryLines(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(s, "\n") {
		q := strings.TrimSpace(line)
		q = doc2qPrefixRe.ReplaceAllString(q, "")
		q = strings.TrimSpace(q)
		if q == "" || len([]rune(q)) < 4 || seen[q] {
			continue
		}
		seen[q] = true
		out = append(out, q)
		if len(out) >= 5 {
			break
		}
	}
	return out
}
