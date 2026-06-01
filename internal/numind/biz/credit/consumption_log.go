package credit

import (
	"context"
	"fmt"
	"time"
)

// ConsumptionLogItem 是「积分消耗记录」单行展示 DTO。
// json tag 即用户端 API 字段。
type ConsumptionLogItem struct {
	ID          uint64    `json:"id"`
	Action      string    `json:"action"`       // 机读 operation（如 sop_run）
	ActionLabel string    `json:"action_label"` // 中文展示名（未知回退裸 operation）
	Credits     int64     `json:"credits"`      // 本次真实消耗积分（= actual_cost_cents）
	CreatedAt   time.Time `json:"created_at"`
}

// operationLabels：机读 operation → 中文展示名。未命中由 operationLabel 回退裸值。
var operationLabels = map[string]string{
	"sop_run":          "SOP 执行",
	"sop_chat":         "SOP 对话",
	"salesrag_chat":    "销售对话",
	"chatbot_chat":     "智能对话",
	"profile_analysis": "客户画像分析",
	"file_parse":       "文件解析",
	"style_analysis":   "风格分析",
	"ocr":              "文字识别",
	"agent_test":       "智能体运行",
}

// operationLabel 返回 op 的中文展示名；未知 operation 回退展示裸值（不报错）。
func operationLabel(op string) string {
	if label, ok := operationLabels[op]; ok {
		return label
	}
	return op
}

// ListConsumptionLog 见 ICreditService 接口注释。数据源 credit_reservation
// （status=reconciled, actual_cost_cents>0）；credits = actual_cost_cents
// （= reserved_credits + delta，对账后真实净扣减）。
func (s *creditService) ListConsumptionLog(ctx context.Context, userID uint, page, pageSize int) ([]ConsumptionLogItem, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	rows, total, err := s.store.Credits().ListReconciledReservationsByUser(ctx, userID, offset, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("ListConsumptionLog: %w", err)
	}

	items := make([]ConsumptionLogItem, 0, len(rows))
	for _, r := range rows {
		var credits int64
		if r.ActualCostCents != nil {
			credits = *r.ActualCostCents
		}
		items = append(items, ConsumptionLogItem{
			ID:          r.ID,
			Action:      r.Operation,
			ActionLabel: operationLabel(r.Operation),
			Credits:     credits,
			CreatedAt:   r.CreatedAt,
		})
	}
	return items, total, nil
}
