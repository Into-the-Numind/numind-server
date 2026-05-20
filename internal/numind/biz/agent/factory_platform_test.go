package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformToolFactory_LoadTools(t *testing.T) {
	// nil rag / ds: tools are instantiated but Execute is not called here,
	// so nil dependencies do not panic during construction.
	f := NewPlatformToolFactory(nil, nil)
	tools, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	assert.Len(t, tools, 6)
	assert.Len(t, metadata, 6)
	expected := []string{
		"kb_search",
		"learner_data_query",
		"document_generate",
		"image_gen",
		"bash_exec",
		"get_current_date",
	}
	for i, want := range expected {
		assert.Equal(t, want, tools[i].Name(), "tool[%d]", i)
		assert.Equal(t, want, metadata[i].ToolName, "metadata[%d]", i)
	}
}

func TestPlatformToolFactory_Metadata(t *testing.T) {
	f := NewPlatformToolFactory(nil, nil).(*platformToolFactory)
	assert.Equal(t, "platform-builtin", f.FactoryID())
	assert.Equal(t, "platform", f.Source())
	assert.Equal(t, "平台内置工具", f.DisplayName())
}

func TestPlatformToolFactory_Watch_Noop(t *testing.T) {
	f := NewPlatformToolFactory(nil, nil)
	if err := f.Watch(context.Background(), nil); err != nil {
		t.Error("Watch should be noop, got:", err)
	}
}
