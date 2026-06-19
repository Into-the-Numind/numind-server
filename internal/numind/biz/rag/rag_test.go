package rag

import (
	"context"
	"testing"

	"github.com/spf13/viper"

	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"
)

// fakeRewriter 是测试用 fallback，记录是否被调用。
type fakeRewriter struct {
	called bool
	result port.RewriteResult
}

func (f *fakeRewriter) Rewrite(_ context.Context, query string, _ []string) (port.RewriteResult, error) {
	f.called = true
	if len(f.result.Queries) == 0 {
		return port.RewriteResult{Queries: []string{"FALLBACK:" + query}}, nil
	}
	return f.result, nil
}

// flag 关（默认）→ FlaggedRewriter 走 fallback，不碰 primary（保零回归）。
func TestFlaggedRewriter_FlagOff_UsesFallback(t *testing.T) {
	viper.Reset()
	fb := &fakeRewriter{}
	primary := &fakeRewriter{result: port.RewriteResult{Queries: []string{"PRIMARY"}}}
	fr := NewFlaggedRewriter(primary, fb)

	res, err := fr.Rewrite(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if primary.called {
		t.Fatal("primary must NOT be called when flag off")
	}
	if !fb.called {
		t.Fatal("fallback must be called when flag off")
	}
	if len(res.Queries) != 1 || res.Queries[0] != "FALLBACK:q" {
		t.Fatalf("expected fallback result, got %v", res.Queries)
	}
}

// flag 关 + fallback 为 nil → 返回原 query（等价不改写，chatbot 旧行为）。
func TestFlaggedRewriter_FlagOff_NilFallback_ReturnsRaw(t *testing.T) {
	viper.Reset()
	fr := NewFlaggedRewriter(&fakeRewriter{}, nil)
	res, err := fr.Rewrite(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Queries) != 1 || res.Queries[0] != "hello" {
		t.Fatalf("expected raw query, got %v", res.Queries)
	}
}

// flag 开 → 走 primary。
func TestFlaggedRewriter_FlagOn_UsesPrimary(t *testing.T) {
	viper.Reset()
	viper.Set(FlagUniversalRewriter, true)
	defer viper.Reset()
	primary := &fakeRewriter{result: port.RewriteResult{Queries: []string{"PRIMARY"}}}
	fb := &fakeRewriter{}
	fr := NewFlaggedRewriter(primary, fb)

	res, err := fr.Rewrite(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !primary.called || fb.called {
		t.Fatalf("expected primary called, fallback not; got primary=%v fb=%v", primary.called, fb.called)
	}
	if len(res.Queries) != 1 || res.Queries[0] != "PRIMARY" {
		t.Fatalf("expected primary result, got %v", res.Queries)
	}
}

// flag 关（默认）→ Gate 放行（fail-open），不碰 LLM。
func TestGate_FlagOff_AllowsWithoutLLM(t *testing.T) {
	viper.Reset()
	g := NewGate()
	ok, reason, err := g.CanAnswer(context.Background(), "q", []domain.KnowledgeChunk{{Content: "x"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("gate must allow when flag off, reason=%q", reason)
	}
}

// flag 开但无 evidence → 拒答（无需 LLM）。
func TestGate_FlagOn_NoEvidence_Refuses(t *testing.T) {
	viper.Reset()
	viper.Set(FlagAnswerabilityGate, true)
	defer viper.Reset()
	g := NewGate()
	ok, _, err := g.CanAnswer(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("gate must refuse when flag on and no evidence")
	}
}

func TestDedupeNonEmpty(t *testing.T) {
	got := dedupeNonEmpty([]string{" a ", "a", "", "b", "a", "  ", "c"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestExtractJSON(t *testing.T) {
	cases := map[string]string{
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"prefix {\"a\":1} suffix": `{"a":1}`,
		"{\"a\":{\"b\":2}}":       `{"a":{"b":2}}`,
		"no json here":            "no json here",
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Fatalf("extractJSON(%q)=%q want %q", in, got, want)
		}
	}
}
