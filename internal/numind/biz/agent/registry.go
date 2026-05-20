package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/datatypes"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// AgentToolRegistry manages ToolFactory registration, loading, and lookup.
type AgentToolRegistry interface {
	RegisterFactory(f ToolFactory) error
	LoadAll(ctx context.Context) error
	GetTool(name string) (FullTool, bool)
	ListEnabled(ctx context.Context) ([]FullTool, error)
	ListAllTools() []FullTool
}

type agentToolRegistry struct {
	mu        sync.RWMutex
	factories []ToolFactory
	tools     map[string]FullTool
	defStore  store.IToolDefinitionStore
	facStore  store.IToolFactoryRegistryStore
}

// NewAgentToolRegistry creates an AgentToolRegistry backed by the given stores.
func NewAgentToolRegistry(defStore store.IToolDefinitionStore, facStore store.IToolFactoryRegistryStore) AgentToolRegistry {
	return &agentToolRegistry{
		tools:    make(map[string]FullTool),
		defStore: defStore,
		facStore: facStore,
	}
}

// Compile-time assertion.
var _ AgentToolRegistry = (*agentToolRegistry)(nil)

// RegisterFactory appends a ToolFactory to the registry.
func (r *agentToolRegistry) RegisterFactory(f ToolFactory) error {
	if f == nil {
		return fmt.Errorf("RegisterFactory: nil factory")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.factories = append(r.factories, f)
	return nil
}

// LoadAll iterates all registered factories, calls LoadTools on each, validates
// the result, persists ToolDefinition rows via Upsert, and records factory stats.
func (r *agentToolRegistry) LoadAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, f := range r.factories {
		tools, metadata, err := f.LoadTools(ctx)
		if err != nil {
			return fmt.Errorf("LoadAll(%s): %w", f.FactoryID(), err)
		}

		// P2 length assert + name-match assert.
		if len(tools) != len(metadata) {
			return fmt.Errorf("LoadAll(%s): tools/metadata length mismatch %d != %d",
				f.FactoryID(), len(tools), len(metadata))
		}
		for i, m := range metadata {
			if m.ToolName != tools[i].Name() {
				return fmt.Errorf("LoadAll(%s): metadata[%d].ToolName=%q != tools[%d].Name()=%q",
					f.FactoryID(), i, m.ToolName, i, tools[i].Name())
			}
			def := metadataToModel(m)
			if err := r.defStore.Upsert(ctx, def); err != nil {
				// Non-fatal but operationally important: log so DB sync failures are observable.
				// Tool stays out of in-mem registry → GetTool will return false until next LoadAll.
				log.Warnw("AgentToolRegistry.LoadAll: defStore.Upsert failed",
					"factory_id", f.FactoryID(),
					"tool_name", m.ToolName,
					"error", err)
				continue
			}
			r.tools[m.ToolName] = tools[i]
		}

		// P2 single Upsert with LoadedToolsCount + LastLoadedAt.
		now := time.Now()
		_ = r.facStore.Upsert(ctx, &model.ToolFactoryRegistryRow{
			FactoryID:        f.FactoryID(),
			SourceType:       f.Source(),
			DisplayName:      f.DisplayName(),
			IsEnabled:        true,
			LoadedToolsCount: len(tools),
			LastLoadedAt:     &now,
		})
	}
	return nil
}

// GetTool returns the FullTool for the given name (thread-safe).
func (r *agentToolRegistry) GetTool(name string) (FullTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// ListEnabled returns the subset of loaded tools whose tool_definition row has is_enabled=true.
func (r *agentToolRegistry) ListEnabled(ctx context.Context) ([]FullTool, error) {
	defs, err := r.defStore.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FullTool, 0, len(defs))
	for _, d := range defs {
		if t, ok := r.tools[d.ToolName]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// ListAllTools returns all in-memory loaded tools regardless of enabled status.
func (r *agentToolRegistry) ListAllTools() []FullTool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FullTool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// metadataToModel converts ToolMetadata into a model.ToolDefinition for Upsert.
func metadataToModel(m ToolMetadata) *model.ToolDefinition {
	def := &model.ToolDefinition{
		ToolName:                m.ToolName,
		DisplayName:             m.DisplayName,
		Description:             m.Description,
		ToolSource:              m.Source,
		RiskLevel:               defaultStr(m.RiskLevel, "safe"),
		RequiresSandbox:         m.RequiresSandbox,
		RequiresTenantWhitelist: m.RequiresTenantWhitelist,
		Category:                m.Category,
		IsEnabled:               true,
	}
	// ToolMetadata.InputSchema is json.RawMessage (= []byte); datatypes.JSON is also []byte.
	if len(m.InputSchema) > 0 {
		def.InputSchema = datatypes.JSON(m.InputSchema)
	}
	return def
}

func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
