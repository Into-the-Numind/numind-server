package multimodal

import (
	"context"
	"errors"
	"testing"
	"time"

	agentatt "numind-server/internal/numind/biz/agent/attachment"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/capability"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAttStore is a minimal IAgentAttachmentStore for routing tests.
type fakeAttStore struct {
	byIDAndUser map[uint64]*model.AgentAttachment
	byID        map[uint64]*model.AgentAttachment
	ownerErr    error // returned by GetByIDAndUser for ids not in byIDAndUser
}

func (f *fakeAttStore) Create(_ context.Context, _ *model.AgentAttachment) error { return nil }
func (f *fakeAttStore) GetByID(_ context.Context, id uint64) (*model.AgentAttachment, error) {
	if a, ok := f.byID[id]; ok {
		return a, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeAttStore) GetByIDAndUser(_ context.Context, id uint64, _ uint) (*model.AgentAttachment, error) {
	if a, ok := f.byIDAndUser[id]; ok {
		return a, nil
	}
	if f.ownerErr != nil {
		return nil, f.ownerErr
	}
	return nil, errors.New("not found")
}

func (f *fakeAttStore) GetByURLAndUser(_ context.Context, _ string, _ uint) (*model.AgentAttachment, error) {
	return nil, errors.New("not found")
}
func (f *fakeAttStore) UpdateFallback(_ context.Context, _ uint64, _ map[string]interface{}) error {
	return nil
}
func (f *fakeAttStore) ListPendingFallback(_ context.Context, _ time.Time, _ int) ([]model.AgentAttachment, error) {
	return nil, nil
}

func strPtr(s string) *string { return &s }

func imageAtt(id uint64, url string) *model.AgentAttachment {
	return &model.AgentAttachment{ID: id, URL: url, Filename: "chart.png", Modality: agentatt.ModalityImage}
}

func TestBuildPartsWithCaps_VisionInline(t *testing.T) {
	att := imageAtt(1, "https://bucket.cos.ap-shanghai.myqcloud.com/agent-attachments/1/x.png")
	caps := &capability.Capabilities{AcceptsImageInline: true}

	parts, hasInlineImage, err := buildPartsWithCaps(context.Background(), "这张图说明什么", []*model.AgentAttachment{att}, caps, nil)
	require.NoError(t, err)
	assert.True(t, hasInlineImage, "vision model + image should set hasInlineImage")
	require.Len(t, parts, 2)
	assert.Equal(t, aiservice.MessagePartTypeText, parts[0].Type)
	assert.Equal(t, "这张图说明什么", parts[0].Text)
	assert.Equal(t, aiservice.MessagePartTypeImageURL, parts[1].Type)
	require.NotNil(t, parts[1].ImageURL)
	// COS disabled in test env → presign returns the URL unchanged.
	assert.Equal(t, att.URL, parts[1].ImageURL.URL)
}

func TestBuildPartsWithCaps_NonVisionFallback(t *testing.T) {
	att := imageAtt(1, "https://x/y.png")
	att.FallbackReady = true
	att.TextFallback = strPtr("[图片：chart.png，画面描述：一张柱状图]")
	caps := &capability.Capabilities{AcceptsImageInline: false}

	parts, hasInlineImage, err := buildPartsWithCaps(context.Background(), "看看这个", []*model.AgentAttachment{att}, caps, nil)
	require.NoError(t, err)
	assert.False(t, hasInlineImage, "non-vision model must not produce inline image")
	require.Len(t, parts, 2)
	assert.Equal(t, aiservice.MessagePartTypeText, parts[1].Type)
	assert.Equal(t, "[图片：chart.png，画面描述：一张柱状图]", parts[1].Text)
	// No image_url part anywhere → FlattenTextParts yields pure text.
	for _, p := range parts {
		assert.NotEqual(t, aiservice.MessagePartTypeImageURL, p.Type)
	}
}

func TestBuildPartsWithCaps_FallbackPendingTimeout(t *testing.T) {
	att := imageAtt(1, "https://x/y.png") // FallbackReady=false, no store → immediate timeout
	caps := &capability.Capabilities{AcceptsImageInline: false}

	parts, hasInlineImage, err := buildPartsWithCaps(context.Background(), "", []*model.AgentAttachment{att}, caps, nil)
	require.NoError(t, err)
	assert.False(t, hasInlineImage)
	require.Len(t, parts, 1) // empty userMessage → only the fallback part
	assert.Equal(t, aiservice.MessagePartTypeText, parts[0].Type)
	assert.Contains(t, parts[0].Text, "描述生成中")
}

// pollingAttStore returns a not-ready attachment until the readyAfter-th GetByID
// call, then returns a ready one — exercising waitForFallback's polling loop.
type pollingAttStore struct {
	fakeAttStore
	readyAfter int
	calls      int
	ready      *model.AgentAttachment
}

func (p *pollingAttStore) GetByID(_ context.Context, id uint64) (*model.AgentAttachment, error) {
	p.calls++
	if p.calls >= p.readyAfter {
		return p.ready, nil
	}
	return &model.AgentAttachment{ID: id, Modality: agentatt.ModalityImage}, nil
}

func TestBuildPartsWithCaps_FallbackPollsUntilReady(t *testing.T) {
	att := imageAtt(1, "https://x/y.png") // FallbackReady=false → must poll
	ready := imageAtt(1, "https://x/y.png")
	ready.FallbackReady = true
	ready.TextFallback = strPtr("[图片：chart.png，画面描述：一张折线图]")
	store := &pollingAttStore{readyAfter: 2, ready: ready}
	caps := &capability.Capabilities{AcceptsImageInline: false}

	parts, hasInlineImage, err := buildPartsWithCaps(context.Background(), "看", []*model.AgentAttachment{att}, caps, store)
	require.NoError(t, err)
	assert.False(t, hasInlineImage)
	require.Len(t, parts, 2)
	assert.Equal(t, "[图片：chart.png，画面描述：一张折线图]", parts[1].Text)
	assert.GreaterOrEqual(t, store.calls, 2, "should have polled until ready")
}

func TestBuildPartsWithCaps_PDFAlwaysFallback(t *testing.T) {
	// Even if a model reports AcceptsPDFInline=true, PDF must take the text
	// fallback path (mkInlineBlock only emits image_url). review P1 guard.
	att := &model.AgentAttachment{ID: 1, URL: "https://x/y.pdf", Filename: "report.pdf", Modality: agentatt.ModalityPDF}
	att.FallbackReady = true
	att.TextFallback = strPtr("[PDF：report.pdf，提取文本：季度营收...]")
	caps := &capability.Capabilities{AcceptsImageInline: true, AcceptsPDFInline: true}

	parts, hasInlineImage, err := buildPartsWithCaps(context.Background(), "总结", []*model.AgentAttachment{att}, caps, nil)
	require.NoError(t, err)
	assert.False(t, hasInlineImage, "PDF must never produce inline image")
	require.Len(t, parts, 2)
	assert.Equal(t, aiservice.MessagePartTypeText, parts[1].Type)
	assert.Equal(t, "[PDF：report.pdf，提取文本：季度营收...]", parts[1].Text)
}

func TestLoadAttachmentsByIDs_SkipsForeign(t *testing.T) {
	mine := imageAtt(1, "https://x/1.png")
	store := &fakeAttStore{
		byIDAndUser: map[uint64]*model.AgentAttachment{1: mine},
		ownerErr:    errors.New("record not found"), // id 2 belongs to someone else
	}
	got := LoadAttachmentsByIDs(context.Background(), store, []uint64{1, 2}, 42)
	require.Len(t, got, 1)
	assert.Equal(t, uint64(1), got[0].ID)
}

func TestLoadAttachmentsByIDs_EmptyAndNil(t *testing.T) {
	assert.Nil(t, LoadAttachmentsByIDs(context.Background(), &fakeAttStore{}, nil, 1))
	assert.Nil(t, LoadAttachmentsByIDs(context.Background(), nil, []uint64{1}, 1))
}

func TestFlattenTextParts(t *testing.T) {
	parts := []aiservice.MessagePart{
		{Type: aiservice.MessagePartTypeText, Text: "hello"},
		{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "u"}},
		{Type: aiservice.MessagePartTypeText, Text: "world"},
	}
	assert.Equal(t, "hello\nworld", FlattenTextParts(parts))
}
