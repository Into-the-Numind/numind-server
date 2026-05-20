package validators

import (
	"context"

	"numind-server/internal/numind/biz/permission"
)

type ClassifierPlaceholder struct{}

func NewClassifierPlaceholder() permission.Validator { return &ClassifierPlaceholder{} }
func (v *ClassifierPlaceholder) ID() string          { return "ClassifierPlaceholder" }

// v1 永远 passthrough（#14 真实实装：异步 qwen-turbo classifier）
func (v *ClassifierPlaceholder) Validate(_ context.Context, _ permission.PermissionRequest) permission.PermissionResult {
	return permission.Passthrough(v.ID(), permission.DecisionReasonClassifier, "v1 stub")
}
