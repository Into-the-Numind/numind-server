package compact

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNullAttachmentReinjector_ReturnsSystemPromptUnchanged(t *testing.T) {
	r := &NullAttachmentReinjector{}
	ctx := context.Background()
	got, err := r.Reinject(ctx, "hello", 42)
	require.NoError(t, err)
	assert.Equal(t, "hello", got)
}

func TestNullAttachmentReinjector_EmptyPrompt(t *testing.T) {
	r := &NullAttachmentReinjector{}
	got, err := r.Reinject(context.Background(), "", 1)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}
