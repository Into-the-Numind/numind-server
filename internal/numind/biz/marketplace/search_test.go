package marketplace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBooleanModeQuery_Empty(t *testing.T) {
	assert.Equal(t, "", booleanModeQuery(""), "empty in → empty out")
}

func TestBooleanModeQuery_SingleToken(t *testing.T) {
	assert.Equal(t, "+销售*", booleanModeQuery("销售"))
}

func TestBooleanModeQuery_MultipleTokens(t *testing.T) {
	assert.Equal(t, "+销售* +调研*", booleanModeQuery("销售 调研"))
}

func TestBooleanModeQuery_StripsBooleanOperators(t *testing.T) {
	// User-typed + - * " ( ) ~ < > @ are all special in BOOLEAN MODE and
	// must be stripped before re-applying our own + and *.
	assert.Equal(t, "+销售*", booleanModeQuery("+销售"))
	assert.Equal(t, "+销售*", booleanModeQuery("-销售"))
	assert.Equal(t, "+销售*", booleanModeQuery(`"销售"`))
	assert.Equal(t, "+销售*", booleanModeQuery("(销售)"))
	assert.Equal(t, "+销售*", booleanModeQuery("~销售"))
	assert.Equal(t, "+销售*", booleanModeQuery("<销售>"))
	assert.Equal(t, "+销售*", booleanModeQuery("@销售"))
	assert.Equal(t, "+销售*", booleanModeQuery("*销售*"))
}

func TestBooleanModeQuery_OnlyOperators_Empty(t *testing.T) {
	// "***" → tokens=["***"] → cleaned="" → dropped → final="".
	// Caller should branch and skip MATCH AGAINST when result is empty.
	assert.Equal(t, "", booleanModeQuery("***"))
	assert.Equal(t, "", booleanModeQuery(`"" + -`))
}

func TestBooleanModeQuery_MixedValidAndOperators(t *testing.T) {
	// "销售 +调研" → 2 tokens; both clean to non-empty; both prefix-AND'd.
	assert.Equal(t, "+销售* +调研*", booleanModeQuery("销售 +调研"))
}

func TestBooleanModeQuery_WhitespaceCollapsed(t *testing.T) {
	// Multiple whitespace → strings.Fields collapses; one token survives per actual word.
	assert.Equal(t, "+a* +b*", booleanModeQuery("  a   \t\n  b  "))
}

func TestBooleanModeQuery_ASCII(t *testing.T) {
	assert.Equal(t, "+sales*", booleanModeQuery("sales"))
	assert.Equal(t, "+sales* +research*", booleanModeQuery("sales research"))
}
