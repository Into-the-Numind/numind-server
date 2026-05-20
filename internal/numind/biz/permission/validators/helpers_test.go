package validators

import (
	"context"
	"encoding/json"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// fakeFullTool embeds BaseTool (31 default methods) and overrides the 5 required ones.
// Extra fields control behavior flags for tests.
type fakeFullTool struct {
	agent.BaseTool
	name          string
	isDestructive bool
	isReadOnly    bool
}

func newFakeTool(name string) *fakeFullTool {
	return &fakeFullTool{name: name, isReadOnly: true}
}

func newFakeDestructiveTool(name string) *fakeFullTool {
	return &fakeFullTool{name: name, isDestructive: true}
}

func (f *fakeFullTool) Name() string           { return f.name }
func (f *fakeFullTool) Description() string    { return "" }
func (f *fakeFullTool) UserFacingName() string { return f.name }
func (f *fakeFullTool) NarrationVerb() string  { return "execute" }
func (f *fakeFullTool) Execute(_ context.Context, _ agent.ToolInput) (agent.ToolResult, error) {
	return nil, nil
}
func (f *fakeFullTool) IsDestructive() bool { return f.isDestructive }
func (f *fakeFullTool) IsReadOnly() bool    { return f.isReadOnly }

var _ agent.FullTool = (*fakeFullTool)(nil) // compile-time check

// fakeAgentPermissionStore is a fake IAgentPermissionStore for tests.
type fakeAgentPermissionStore struct {
	rules []model.AgentPermissionConfig
	err   error
}

var _ store.IAgentPermissionStore = (*fakeAgentPermissionStore)(nil)

func (f *fakeAgentPermissionStore) ListActiveByParent(_ context.Context, _ uint) ([]model.AgentPermissionConfig, error) {
	return f.rules, f.err
}

func (f *fakeAgentPermissionStore) CreateRule(_ context.Context, _ *model.AgentPermissionConfig) error {
	return nil
}

func (f *fakeAgentPermissionStore) CreateDecisionLog(_ context.Context, _ *model.AgentPermissionDecisionLog) error {
	return nil
}

// fakeAgentDefinitionStore is a fake IAgentDefinitionStore for tests.
type fakeAgentDefinitionStore struct {
	definition *model.AgentDefinition
	err        error
}

var _ store.IAgentDefinitionStore = (*fakeAgentDefinitionStore)(nil)

func (f *fakeAgentDefinitionStore) Create(_ context.Context, _ *model.AgentDefinition) error {
	return nil
}
func (f *fakeAgentDefinitionStore) CreateTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinition) error {
	return nil
}
func (f *fakeAgentDefinitionStore) GetByID(_ context.Context, _ uint64) (*model.AgentDefinition, error) {
	return f.definition, f.err
}
func (f *fakeAgentDefinitionStore) GetByIDIncludeInactive(_ context.Context, _ uint64) (*model.AgentDefinition, error) {
	return f.definition, f.err
}
func (f *fakeAgentDefinitionStore) ListByParent(_ context.Context, _ uint, _ bool, _, _ int) ([]model.AgentDefinition, int64, error) {
	return nil, 0, nil
}
func (f *fakeAgentDefinitionStore) Update(_ context.Context, _ *model.AgentDefinition) error {
	return nil
}
func (f *fakeAgentDefinitionStore) UpdateTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinition) error {
	return nil
}
func (f *fakeAgentDefinitionStore) SoftDelete(_ context.Context, _ uint64) error { return nil }
func (f *fakeAgentDefinitionStore) SoftDeleteTx(_ context.Context, _ *gorm.DB, _ uint64) error {
	return nil
}
func (f *fakeAgentDefinitionStore) WriteHistory(_ context.Context, _ *model.AgentDefinitionHistory) error {
	return nil
}
func (f *fakeAgentDefinitionStore) WriteHistoryTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinitionHistory) error {
	return nil
}
func (f *fakeAgentDefinitionStore) ListHistory(_ context.Context, _ uint64) ([]model.AgentDefinitionHistory, error) {
	return nil, nil
}
func (f *fakeAgentDefinitionStore) GetHistoryByVersion(_ context.Context, _ uint64, _ uint) (*model.AgentDefinitionHistory, error) {
	return nil, nil
}
func (f *fakeAgentDefinitionStore) MaxVersion(_ context.Context, _ uint64) (uint, error) {
	return 0, nil
}

// mustJSON marshals a map to a JSON string for use as InputJSON in tests.
func mustJSON(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
