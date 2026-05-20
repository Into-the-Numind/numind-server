package validators

import (
	"context"
	"testing"

	"numind-server/internal/numind/biz/permission"
)

func TestClassifierPlaceholder_AlwaysPassthrough(t *testing.T) {
	v := NewClassifierPlaceholder()
	req := permission.PermissionRequest{
		Tool:      newFakeTool("bash_exec"),
		InputJSON: mustJSON(map[string]any{"command": "rm -rf /"}),
	}
	got := v.Validate(context.Background(), req)
	if got.Behavior != permission.BehaviorPassthrough {
		t.Errorf("want passthrough, got %q", got.Behavior)
	}
	if got.DecisionReason != permission.DecisionReasonClassifier {
		t.Errorf("want reason=classifier, got %q", got.DecisionReason)
	}
}

func TestClassifierPlaceholder_ID(t *testing.T) {
	v := NewClassifierPlaceholder()
	if v.ID() != "ClassifierPlaceholder" {
		t.Errorf("want ID ClassifierPlaceholder, got %q", v.ID())
	}
}
