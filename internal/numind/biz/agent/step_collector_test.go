package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStepCollector_AddListOrderAndNilSafe(t *testing.T) {
	// nil receiver: add no-ops, list returns nil.
	var nilC *stepCollector
	nilC.add("x", "y", time.Now())
	assert.Nil(t, nilC.list())
	assert.Nil(t, stepCollectorFrom(context.Background())) // no collector on ctx

	ctx := withStepCollector(context.Background())
	c := stepCollectorFrom(ctx)
	require.NotNil(t, c)

	base := time.Now()
	c.add("step0", "r0", base)
	c.add("step1", "", base.Add(time.Millisecond))

	got := c.list()
	require.Len(t, got, 2)
	assert.Equal(t, "step0", got[0].Content)
	assert.Equal(t, "r0", got[0].Reasoning)
	assert.Equal(t, "step1", got[1].Content)

	// list returns a copy — mutating it must not affect the collector.
	got[0].Content = "mutated"
	assert.Equal(t, "step0", c.list()[0].Content)
}

func TestStepCollector_DropsFullyEmptySteps(t *testing.T) {
	ctx := withStepCollector(context.Background())
	c := stepCollectorFrom(ctx)
	c.add("", "", time.Now()) // empty content AND reasoning → dropped
	c.add("", "has reasoning", time.Now())
	c.add("has content", "", time.Now())
	got := c.list()
	require.Len(t, got, 2)
}

func TestStepCollector_ReasoningSoftCap(t *testing.T) {
	ctx := withStepCollector(context.Background())
	c := stepCollectorFrom(ctx)
	long := strings.Repeat("思", maxStepReasoningRunes+500)
	c.add("answer", long, time.Now())
	got := c.list()
	require.Len(t, got, 1)
	r := []rune(got[0].Reasoning)
	assert.LessOrEqual(t, len(r), maxStepReasoningRunes+20) // capped + short marker
	assert.Contains(t, got[0].Reasoning, "已截断")
	// content is NOT capped
	assert.Equal(t, "answer", got[0].Content)
}

func TestStepCollector_MaxStepsCap(t *testing.T) {
	ctx := withStepCollector(context.Background())
	c := stepCollectorFrom(ctx)
	for i := 0; i < maxSteps+10; i++ {
		c.add("s", "", time.Now())
	}
	assert.Len(t, c.list(), maxSteps)
	assert.Equal(t, 10, c.Dropped())
}
