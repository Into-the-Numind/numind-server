package agent

import (
	"context"

	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/numind/store"
)

type platformToolFactory struct {
	rag salesrag.SalesRAGBiz
	ds  store.IStore
}

// NewPlatformToolFactory returns a ToolFactory that loads all platform built-in tools.
func NewPlatformToolFactory(rag salesrag.SalesRAGBiz, ds store.IStore) ToolFactory {
	return &platformToolFactory{rag: rag, ds: ds}
}

// Compile-time assertion.
var _ ToolFactory = (*platformToolFactory)(nil)

func (f *platformToolFactory) FactoryID() string   { return "platform-builtin" }
func (f *platformToolFactory) Source() string      { return "platform" }
func (f *platformToolFactory) DisplayName() string { return "平台内置工具" }

// LoadTools instantiates all 6 platform built-in FullTool instances together
// with their ToolMetadata for tool_definition upsert.
//
// Nil-safety: f.rag / f.ds may be nil in unit-test contexts (TestPlatformToolFactory_LoadTools
// passes nil to verify the tool list / metadata wiring without spinning up real biz/store).
// Tools constructed with nil deps will panic only if Execute is called; LoadTools itself
// must never panic.
func (f *platformToolFactory) LoadTools(_ context.Context) ([]FullTool, []ToolMetadata, error) {
	var usersGetter userByIDGetter
	if f.ds != nil {
		usersGetter = f.ds.Users()
	}
	tools := []FullTool{
		&kbSearchTool{rag: f.rag},
		&learnerDataQueryTool{users: usersGetter},
		&documentGenerateTool{},
		&imageGenTool{},
		&bashExecTool{},
		&getCurrentDateTool{},
	}
	metadata := []ToolMetadata{
		{ToolName: "kb_search", DisplayName: "知识库检索", Description: "Search the knowledge base.", Source: "platform", Category: "RAG"},
		{ToolName: "learner_data_query", DisplayName: "学员档案", Description: "Query learner profile.", Source: "platform", Category: "查询", RiskLevel: "moderate"},
		{ToolName: "document_generate", DisplayName: "文档生成", Description: "[stub] Generate documents.", Source: "platform", Category: "生成"},
		{ToolName: "image_gen", DisplayName: "图像生成", Description: "[stub] Generate images.", Source: "platform", Category: "多媒体", RequiresSandbox: true},
		{ToolName: "bash_exec", DisplayName: "代码执行", Description: "[stub] Execute shell.", Source: "platform", Category: "代码", RiskLevel: "dangerous", RequiresSandbox: true},
		{ToolName: "get_current_date", DisplayName: "当前日期", Description: "Return today's date.", Source: "platform", Category: "查询"},
	}
	return tools, metadata, nil
}

// Watch is a no-op in v1; dynamic reloading is deferred to a future task.
func (f *platformToolFactory) Watch(_ context.Context, _ func(diff ToolDiff)) error {
	return nil
}
