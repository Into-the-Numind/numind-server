package compliance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInjectionDetector_Detect_AllKeywords(t *testing.T) {
	d := NewInjectionDetector(nil) // mock
	ctx := context.Background()
	for _, kw := range injectionKeywords {
		t.Run("keyword="+kw, func(t *testing.T) {
			input := "前缀 " + kw + " 后缀"
			hit, matched, err := d.Detect(ctx, input)
			require.NoError(t, err)
			assert.True(t, hit)
			assert.Equal(t, kw, matched)
		})
	}
}

func TestInjectionDetector_Detect_CaseInsensitive(t *testing.T) {
	d := NewInjectionDetector(nil)
	hit, matched, err := d.Detect(context.Background(), "IGNORE PREVIOUS")
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, "ignore previous", matched)
}

func TestInjectionDetector_Detect_NoMatch(t *testing.T) {
	d := NewInjectionDetector(nil)
	hit, matched, err := d.Detect(context.Background(), "帮我看下这道数学题")
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Equal(t, "", matched)
}

func TestInjectionDetector_Detect_MixedChinese(t *testing.T) {
	d := NewInjectionDetector(nil)
	hit, _, err := d.Detect(context.Background(), "Forget your instructions 忽略之前")
	require.NoError(t, err)
	assert.True(t, hit)
}

func TestInjectionDetector_Detect_ClassifierError_FailOpen(t *testing.T) {
	d := &InjectionDetector{classifier: errClassifier{}}
	hit, _, err := d.Detect(context.Background(), "完全合法的输入")
	require.Error(t, err)
	assert.False(t, hit)
}

type errClassifier struct{}

func (errClassifier) Classify(ctx context.Context, input string) (bool, error) {
	return false, errors.New("classifier failure")
}

func TestMockClassifier_AlwaysFalse(t *testing.T) {
	m := NewMockClassifier()
	hit, err := m.Classify(context.Background(), "anything")
	require.NoError(t, err)
	assert.False(t, hit)
}

func TestWrapInputFence(t *testing.T) {
	out := WrapInputFence("uploaded_file", "本周笔记.xlsx", "[content]")
	assert.True(t, strings.HasPrefix(out, "<external_data "))
	assert.Contains(t, out, `source="uploaded_file"`)
	assert.Contains(t, out, `name="本周笔记.xlsx"`)
	assert.Contains(t, out, `trust="low"`)
	assert.True(t, strings.HasSuffix(out, "</external_data>"))
}
