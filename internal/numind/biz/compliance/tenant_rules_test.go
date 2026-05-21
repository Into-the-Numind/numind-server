package compliance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// fakeTenantStore captures List calls; returns canned rules or error.
type fakeTenantStore struct {
	mu        sync.Mutex
	callCount int
	rules     []*model.ComplianceRule
	err       error
}

func (f *fakeTenantStore) ListRulesByParent(ctx context.Context, parentUserID uint, activeOnly bool) ([]*model.ComplianceRule, error) {
	f.mu.Lock()
	f.callCount++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.rules, nil
}

func (f *fakeTenantStore) GetRule(ctx context.Context, id uint64) (*model.ComplianceRule, error) {
	return nil, nil
}
func (f *fakeTenantStore) CreateRule(ctx context.Context, r *model.ComplianceRule) error { return nil }
func (f *fakeTenantStore) UpdateRule(ctx context.Context, id uint64, u map[string]interface{}) error {
	return nil
}
func (f *fakeTenantStore) SoftDeleteRule(ctx context.Context, id uint64) error { return nil }
func (f *fakeTenantStore) WriteAuditLog(ctx context.Context, e *model.ComplianceAuditLog) error {
	return nil
}

func TestTenantRuleProvider_GetActiveRules_CacheMiss(t *testing.T) {
	fs := &fakeTenantStore{rules: []*model.ComplianceRule{{ID: 1, RuleType: "forbid_brand", RuleText: "Bank X", Priority: 100}}}
	p := NewTenantRuleProvider(fs, NewTTLCache(10, time.Minute))
	rules, err := p.GetActiveRules(context.Background(), 42)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, 1, fs.callCount)
}

func TestTenantRuleProvider_GetActiveRules_CacheHit(t *testing.T) {
	fs := &fakeTenantStore{rules: []*model.ComplianceRule{{ID: 1, RuleType: "forbid_brand", RuleText: "Bank X", Priority: 100}}}
	cache := NewTTLCache(10, time.Minute)
	p := NewTenantRuleProvider(fs, cache)
	_, err := p.GetActiveRules(context.Background(), 42)
	require.NoError(t, err)
	// second call should hit cache
	rules, err := p.GetActiveRules(context.Background(), 42)
	require.NoError(t, err)
	assert.Len(t, rules, 1)
	assert.Equal(t, 1, fs.callCount, "store called only once due to cache hit")
}

func TestTenantRuleProvider_GetActiveRules_StoreError(t *testing.T) {
	fs := &fakeTenantStore{err: errors.New("db down")}
	p := NewTenantRuleProvider(fs, NewTTLCache(10, time.Minute))
	_, err := p.GetActiveRules(context.Background(), 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TenantRuleProvider.GetActiveRules")
}

func TestTenantRuleProvider_GetActiveRules_Sorting(t *testing.T) {
	now := time.Now()
	fs := &fakeTenantStore{rules: []*model.ComplianceRule{
		{ID: 1, Priority: 200, CreatedAt: now.Add(-2 * time.Hour)}, // lower prio
		{ID: 2, Priority: 100, CreatedAt: now.Add(-1 * time.Hour)}, // higher prio, older
		{ID: 3, Priority: 100, CreatedAt: now},                     // higher prio, newer
	}}
	p := NewTenantRuleProvider(fs, NewTTLCache(10, time.Minute))
	rules, err := p.GetActiveRules(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, rules, 3)
	// priority ASC first; same priority → created_at DESC
	assert.Equal(t, uint64(3), rules[0].ID) // priority 100, newest
	assert.Equal(t, uint64(2), rules[1].ID) // priority 100, older
	assert.Equal(t, uint64(1), rules[2].ID) // priority 200
}

func TestTenantRuleProvider_RenderFenced_Empty(t *testing.T) {
	p := &TenantRuleProvider{}
	out := p.RenderFenced(42, nil)
	assert.Equal(t, "", out)
}

func TestTenantRuleProvider_RenderFenced_AllRuleTypes(t *testing.T) {
	rules := []*model.ComplianceRule{
		{RuleType: model.ComplianceRuleTypeForbidTopic, RuleText: "X1"},
		{RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "X2"},
		{RuleType: model.ComplianceRuleTypeForbidPhrase, RuleText: "X3"},
		{RuleType: model.ComplianceRuleTypeCustom, RuleText: "X4"},
	}
	p := &TenantRuleProvider{}
	out := p.RenderFenced(42, rules)
	assert.True(t, strings.HasPrefix(out, "<tenant_hard_rules parent_id=\"42\">\n"))
	assert.Contains(t, out, "- 禁讨论话题：X1")
	assert.Contains(t, out, "- 禁讨论品牌：X2")
	assert.Contains(t, out, "- 禁出现短语：X3")
	assert.Contains(t, out, "- 自定义规则：X4")
	assert.True(t, strings.HasSuffix(out, "</tenant_hard_rules>\n"))
}

func TestTenantRuleProvider_MatchOutput_HitForbidBrand(t *testing.T) {
	rules := []*model.ComplianceRule{
		{ID: 7, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "Bank X"},
	}
	p := &TenantRuleProvider{}
	hit, matched := p.MatchOutput(rules, "我推荐 Bank X 的理财产品")
	require.NotNil(t, hit)
	assert.Equal(t, uint64(7), hit.ID)
	assert.Equal(t, "Bank X", matched)
}

func TestTenantRuleProvider_MatchOutput_HitForbidPhrase(t *testing.T) {
	rules := []*model.ComplianceRule{
		{ID: 8, RuleType: model.ComplianceRuleTypeForbidPhrase, RuleText: "Guaranteed Return"},
	}
	p := &TenantRuleProvider{}
	hit, matched := p.MatchOutput(rules, "this is GUARANTEED RETURN") // case insensitive
	require.NotNil(t, hit)
	assert.Equal(t, uint64(8), hit.ID)
	assert.Equal(t, "Guaranteed Return", matched)
}

func TestTenantRuleProvider_MatchOutput_NoHit(t *testing.T) {
	rules := []*model.ComplianceRule{
		{ID: 1, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "Foo"},
	}
	p := &TenantRuleProvider{}
	hit, matched := p.MatchOutput(rules, "完全无关的输出")
	assert.Nil(t, hit)
	assert.Equal(t, "", matched)
}

func TestTenantRuleProvider_MatchOutput_SkipsTopicAndCustom(t *testing.T) {
	// forbid_topic / custom 不参与精确匹配（v1 行为）
	rules := []*model.ComplianceRule{
		{ID: 1, RuleType: model.ComplianceRuleTypeForbidTopic, RuleText: "X"},
		{ID: 2, RuleType: model.ComplianceRuleTypeCustom, RuleText: "Y"},
	}
	p := &TenantRuleProvider{}
	hit, _ := p.MatchOutput(rules, "X and Y are mentioned")
	assert.Nil(t, hit, "forbid_topic and custom should not trigger precise match")
}
