package compliance

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateOutput_AllForbiddenFences(t *testing.T) {
	for _, fence := range outputForbiddenFences {
		t.Run("fence="+fence, func(t *testing.T) {
			out := "some text " + fence + " injected"
			hit, matched := ValidateOutput(out)
			assert.True(t, hit)
			assert.Equal(t, fence, matched)
		})
	}
}

func TestValidateOutput_CaseInsensitive(t *testing.T) {
	hit, matched := ValidateOutput("text <SYSTEM> here")
	assert.True(t, hit)
	assert.Equal(t, "<system>", matched)
}

func TestValidateOutput_NoMatch(t *testing.T) {
	hit, matched := ValidateOutput("这是一个完全合法的回答")
	assert.False(t, hit)
	assert.Equal(t, "", matched)
}

func TestValidateOutput_EmptyString(t *testing.T) {
	hit, matched := ValidateOutput("")
	assert.False(t, hit)
	assert.Equal(t, "", matched)
}

func TestValidateOutput_FirstMatchWins(t *testing.T) {
	// 多个 fence 命中，返回第一个（列表顺序）
	hit, matched := ValidateOutput("<memory> and <system>")
	assert.True(t, hit)
	// First in outputForbiddenFences slice should win;
	// "<system>" comes first → matched should be "<system>" since loop order matches slice order
	assert.Equal(t, "<system>", matched)
}
