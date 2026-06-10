package port

import "context"

// QueryRewriter 领域无关的查询改写（指代消解 + 多 query + HyDE），剥离销售意图概念。
type QueryRewriter interface {
	Rewrite(ctx context.Context, query string, history []string) (RewriteResult, error)
}

// RewriteResult 改写结果。
type RewriteResult struct {
	Queries []string // 改写出的检索查询（含指代消解）
	HyDE    string   // 可选的假设性文档片段，空表示不用
}
