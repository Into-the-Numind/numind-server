package adapter

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/aiservice"
)

// NewGatewayEmbedder 返回一个走 aiservice 网关的 embedder 闭包。
//
// 行为与原先 biz.go 内联闭包逐字等价：对单条文本调用
// aiservice.Embed(ctx, taskID, {Texts: [text], Dimension: dimension})，
// 返回首条向量；空响应视为错误。
//
// 调用方需保证 ctx 已携带 aismw.WithUserID + aiservice.WithSkipLegacyBilling
// （由 pipeline.worker() 注入），本工厂不做任何 ctx 修饰。
//
// dimension 由路由到的向量库 collection schema 决定（SalesRAG prod 固定 2048，
// 必须匹配 task_profile.requirements.dimension 与 ai_service.capability_json.dimension）。
func NewGatewayEmbedder(taskID string, dimension int) func(context.Context, string) ([]float32, error) {
	return func(ctx context.Context, text string) ([]float32, error) {
		resp, err := aiservice.Embed(ctx, taskID, aiservice.EmbedRequest{
			Texts:     []string{text},
			Dimension: dimension,
		})
		if err != nil {
			return nil, err
		}
		if len(resp.Embeddings) == 0 {
			return nil, fmt.Errorf("salesrag embed: empty embedding response")
		}
		return resp.Embeddings[0], nil
	}
}
