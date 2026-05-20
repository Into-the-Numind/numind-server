package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// echoTool 是 mock 工具：input → input。
type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "Echo input back unchanged" }
func (echoTool) Run(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	return input, nil
}

// errTool 是 mock 工具：始终返回 error。
type errTool struct{}

func (errTool) Name() string        { return "err" }
func (errTool) Description() string { return "always fails" }
func (errTool) Run(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("boom")
}

func TestEinoToolAdapter_Info(t *testing.T) {
	a := AdaptTool(echoTool{})
	info, err := a.Info(context.Background())
	if err != nil {
		t.Fatalf("Info err: %v", err)
	}
	if info.Name != "echo" {
		t.Errorf("Name = %q, want echo", info.Name)
	}
	if info.Desc == "" {
		t.Error("Desc empty")
	}
}

func TestEinoToolAdapter_InvokableRun_Echo(t *testing.T) {
	a := AdaptTool(echoTool{})
	out, err := a.InvokableRun(context.Background(), `{"hello":"world"}`)
	if err != nil {
		t.Fatalf("InvokableRun err: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("out=%q, want contain 'hello'", out)
	}
}

func TestEinoToolAdapter_InvokableRun_Error(t *testing.T) {
	a := AdaptTool(errTool{})
	_, err := a.InvokableRun(context.Background(), `{}`)
	if err == nil {
		t.Error("expected err")
	}
}
