package ingest

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/retrieval/domain"
)

func TestParseDoc2QueryLines(t *testing.T) {
	raw := "1. 客户说太贵了怎么回应？\n2、拿不到offer退多少钱\n- 分期付款可以吗\n客户说太贵了怎么回应？\n好\n• 内推靠谱吗"
	qs := parseDoc2QueryLines(raw)
	// 去编号/符号前缀、去重（"客户说太贵了"出现两次）、过短("好")丢弃。
	if len(qs) != 4 {
		t.Fatalf("expected 4 parsed questions, got %d: %v", len(qs), qs)
	}
	for _, q := range qs {
		if strings.HasPrefix(q, "1.") || strings.HasPrefix(q, "-") || strings.HasPrefix(q, "•") {
			t.Errorf("prefix not stripped: %q", q)
		}
	}
	if qs[0] != "客户说太贵了怎么回应？" {
		t.Errorf("unexpected first question: %q", qs[0])
	}
}

func TestParseDoc2QueryLines_DigitLeadingNotMangled(t *testing.T) {
	// 数字开头的合法问题不应被当编号前缀剥掉。
	qs := parseDoc2QueryLines("3年内能拿到offer吗\n5万预算够不够\n1. 这条是编号问题啊")
	if len(qs) != 3 {
		t.Fatalf("expected 3, got %d: %v", len(qs), qs)
	}
	if qs[0] != "3年内能拿到offer吗" || qs[1] != "5万预算够不够" {
		t.Errorf("digit-leading questions mangled: %v", qs)
	}
	if qs[2] != "这条是编号问题啊" {
		t.Errorf("numbered prefix not stripped: %q", qs[2])
	}
}

func TestParseDoc2QueryLines_Cap5(t *testing.T) {
	raw := "问题一啊\n问题二啊\n问题三啊\n问题四啊\n问题五啊\n问题六啊\n问题七啊"
	if n := len(parseDoc2QueryLines(raw)); n != 5 {
		t.Errorf("expected cap at 5, got %d", n)
	}
}

func TestDoc2Query_FlagOffNoop(t *testing.T) {
	viper.Set(FlagDoc2Query, false)
	defer viper.Set(FlagDoc2Query, false)

	g := NewDoc2QueryGenerator()
	chunks := []*domain.KnowledgeChunk{{ID: "1_0", Content: "内容", EmbedText: "面包屑\n\n内容"}}
	g.MaybeAugment(context.Background(), chunks)
	// flag 关 → EmbedText 不变（不调 LLM，零回归）。
	if chunks[0].EmbedText != "面包屑\n\n内容" {
		t.Errorf("flag off should not modify EmbedText, got %q", chunks[0].EmbedText)
	}

	// nil generator 也 no-op 不 panic。
	var nilGen *Doc2QueryGenerator
	nilGen.MaybeAugment(context.Background(), chunks)
}
