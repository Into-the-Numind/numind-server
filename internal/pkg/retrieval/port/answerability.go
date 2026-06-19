package port

import (
	"context"

	"numind-server/internal/pkg/retrieval/domain"
)

// AnswerabilityGate 可答性判定门：领域无关地判断"检索到的资料能否回答该问题"。
//
// 它与 rerank 阈值【解耦】——阈值/召回管"找得全"，门管"该拒就拒"，两者各由独立机制
// 决定，破解"召回↑必伴拒答↓"的死结：检索可放开（低阈值/改写/HyDE 拉满召回），
// 由门兜住拒答（资料答不了就拒），从而召回与拒答不再此消彼长。
type AnswerabilityGate interface {
	// CanAnswer 判断仅凭 chunks 能否实质回答 query。
	//   (true, reason)  → 可答，保留 chunks；
	//   (false, reason) → 应拒答，上层清空 chunks。
	// 实现必须 fail-open：内部出错时返回 (true, ...)，绝不因门故障而阻断检索。
	CanAnswer(ctx context.Context, query string, chunks []domain.KnowledgeChunk) (bool, string, error)
}
