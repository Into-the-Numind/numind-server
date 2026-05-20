package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── fake FullTool for adapter tests ─────────────────────────────────────────

type fakeFullTool struct {
	BaseTool
	name string
	desc string
	out  []byte
	err  error
}

func (f *fakeFullTool) Name() string           { return f.name }
func (f *fakeFullTool) Description() string    { return f.desc }
func (f *fakeFullTool) UserFacingName() string { return f.name }
func (f *fakeFullTool) NarrationVerb() string  { return "执行" }

func (f *fakeFullTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return ToolResult(f.out), nil
}

// ── tests ────────────────────────────────────────────────────────────────────

func TestAdaptFullToEinoTool_Info(t *testing.T) {
	ft := &fakeFullTool{name: "echo", desc: "echoes input"}
	eino := adaptFullToEinoTool(ft)
	info, err := eino.Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "echo", info.Name)
	assert.Equal(t, "echoes input", info.Desc)
}

func TestAdaptFullToEinoTool_InvokableRun_Success(t *testing.T) {
	ft := &fakeFullTool{name: "x", out: []byte(`{"ok":true}`)}
	eino := adaptFullToEinoTool(ft)
	out, err := eino.InvokableRun(context.Background(), `{"input":"hi"}`)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, out)
}

func TestAdaptFullToEinoTool_InvokableRun_Error(t *testing.T) {
	ft := &fakeFullTool{name: "x", err: errors.New("boom")}
	eino := adaptFullToEinoTool(ft)
	_, err := eino.InvokableRun(context.Background(), `{}`)
	require.Error(t, err)
	assert.EqualError(t, err, "boom")
}

func TestAdaptFullToEinoTool_InvokableRun_EmptyResult(t *testing.T) {
	ft := &fakeFullTool{name: "empty", out: []byte(nil)}
	eino := adaptFullToEinoTool(ft)
	out, err := eino.InvokableRun(context.Background(), `{}`)
	require.NoError(t, err)
	assert.Equal(t, "", out)
}
