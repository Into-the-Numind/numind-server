package agent

import (
	"context"
	"testing"
)

func TestBashExecTool_Execute_ReturnsError(t *testing.T) {
	tool := &bashExecTool{}
	_, err := tool.Execute(context.Background(), nil)
	if err == nil {
		t.Error("bash_exec stub should return an error")
	}
}

func TestBashExecTool_IsEnabled_FalseWhenSandboxDisabled(t *testing.T) {
	tool := &bashExecTool{}
	if tool.IsEnabled(ToolConfig{EnableSandbox: false}) {
		t.Error("bash_exec should be disabled when EnableSandbox=false")
	}
}

func TestBashExecTool_IsEnabled_TrueWhenSandboxEnabled(t *testing.T) {
	tool := &bashExecTool{}
	if !tool.IsEnabled(ToolConfig{EnableSandbox: true}) {
		t.Error("bash_exec should be enabled when EnableSandbox=true")
	}
}

func TestBashExecTool_IsDestructive(t *testing.T) {
	tool := &bashExecTool{}
	if !tool.IsDestructive() {
		t.Error("bash_exec should be destructive")
	}
}

func TestBashExecTool_IsReadOnly(t *testing.T) {
	tool := &bashExecTool{}
	if tool.IsReadOnly() {
		t.Error("bash_exec should not be read-only")
	}
}

func TestBashExecTool_Name(t *testing.T) {
	tool := &bashExecTool{}
	if tool.Name() != "bash_exec" {
		t.Errorf("unexpected name: %s", tool.Name())
	}
}
