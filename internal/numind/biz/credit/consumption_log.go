package credit

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ConsumptionLogItem 是「积分消耗记录」单行展示 DTO。
// json tag 即用户端 API 字段。
type ConsumptionLogItem struct {
	ID          uint64    `json:"id"`
	Action      string    `json:"action"`       // 机读 operation（如 sop_run）
	ActionLabel string    `json:"action_label"` // 中文展示名（未知回退裸 operation）
	Credits     int64     `json:"credits"`      // 本次真实消耗积分（= actual_cost_cents）
	DetailName  string    `json:"detail_name"`  // 具体任务名（空=不可解析，前端回退 action_label）
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

	detailMap := s.enrichDetailNames(ctx, userID, rows)

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
			DetailName:  detailMap[r.ID],
			CreatedAt:   r.CreatedAt,
		})
	}
	return items, total, nil
}

// enrichDetailNames resolves a specific task/session name for each reservation
// by parsing reference_id (format: "<prefix>:<id1>[:<id2>]") and performing
// batched GORM lookups (≤1 query per entity table). Returns a map from
// reservation ID → detail name. Missing / unresolvable → "".
//
// Supported prefixes:
//   - sop_run:<runID>:<nodeID>         → "<template.Name> · <node.Name>"
//   - sop_chat:<runID>[:<seq>]         → "<template.Name>"  (legacy colon-3 form tolerated)
//   - sales_session:<id>               → session Title (filtered by user_id — 越权防护)
//   - chatbot_session:<id>             → chatbot config Name
func (s *creditService) enrichDetailNames(ctx context.Context, userID uint, rows []model.CreditReservation) map[uint64]string {
	// Parsed per-row metadata
	type rowMeta struct {
		prefix   string
		runID    uint
		nodeID   uint
		salesID  uint
		cbSessID uint
	}
	metas := make([]rowMeta, len(rows))

	// parseInt parses a decimal string to uint; returns 0 on error.
	parseInt := func(s string) uint {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return 0
		}
		return uint(v) //nolint:gosec // IDs are always positive, safe narrowing
	}

	// Collect IDs for batch lookups (use sets to avoid duplicate IN-list entries).
	nodeIDSet := make(map[uint]struct{})
	runIDSet := make(map[uint]struct{})
	salesIDSet := make(map[uint]struct{})
	cbSessIDSet := make(map[uint]struct{})

	for i, r := range rows {
		if r.ReferenceID == "" {
			continue
		}
		parts := strings.Split(r.ReferenceID, ":")
		if len(parts) < 2 {
			continue
		}
		prefix := parts[0]
		metas[i].prefix = prefix

		switch prefix {
		case "sop_run":
			if len(parts) >= 3 {
				metas[i].runID = parseInt(parts[1])
				metas[i].nodeID = parseInt(parts[2])
				if metas[i].runID > 0 && metas[i].nodeID > 0 {
					nodeIDSet[metas[i].nodeID] = struct{}{}
					runIDSet[metas[i].runID] = struct{}{}
				}
			}
		case "sop_chat":
			// Tolerate legacy format "sop_chat:<run>:<seq>" — parts[1] is always runID
			if len(parts) >= 2 {
				metas[i].runID = parseInt(parts[1])
				if metas[i].runID > 0 {
					runIDSet[metas[i].runID] = struct{}{}
				}
			}
		case "sales_session":
			id := parseInt(parts[1])
			if id > 0 {
				metas[i].salesID = id
				salesIDSet[id] = struct{}{}
			}
		case "chatbot_session":
			id := parseInt(parts[1])
			if id > 0 {
				metas[i].cbSessID = id
				cbSessIDSet[id] = struct{}{}
			}
		}
	}

	// Convert deduped sets to slices for GORM IN queries.
	nodeIDs := make([]uint, 0, len(nodeIDSet))
	for id := range nodeIDSet {
		nodeIDs = append(nodeIDs, id)
	}
	runIDs := make([]uint, 0, len(runIDSet))
	for id := range runIDSet {
		runIDs = append(runIDs, id)
	}
	salesIDs := make([]uint, 0, len(salesIDSet))
	for id := range salesIDSet {
		salesIDs = append(salesIDs, id)
	}
	cbSessIDs := make([]uint, 0, len(cbSessIDSet))
	for id := range cbSessIDSet {
		cbSessIDs = append(cbSessIDs, id)
	}

	db := s.store.DB().WithContext(ctx)

	// ── batch lookups ─────────────────────────────────────────────────────────

	// nodes: id → {Name, TemplateID}
	type nodeRow struct {
		ID         uint
		Name       string
		TemplateID uint
	}
	nodeMap := make(map[uint]nodeRow)
	if len(nodeIDs) > 0 {
		var nodes []nodeRow
		if err := db.Model(&model.SopNode{}).
			Where("id IN ?", nodeIDs).
			Select("id, name, template_id").
			Scan(&nodes).Error; err != nil {
			log.C(ctx).Warnw("enrichDetailNames: sop_node lookup failed", "err", err)
		}
		for _, n := range nodes {
			nodeMap[n.ID] = n
		}
	}

	// sop_runs: id → TemplateID
	type runRow struct {
		ID         uint
		TemplateID uint
	}
	runMap := make(map[uint]uint)
	if len(runIDs) > 0 {
		var runs []runRow
		if err := db.Model(&model.SopRun{}).
			Where("id IN ?", runIDs).
			Select("id, template_id").
			Scan(&runs).Error; err != nil {
			log.C(ctx).Warnw("enrichDetailNames: sop_run lookup failed", "err", err)
		}
		for _, r := range runs {
			runMap[r.ID] = r.TemplateID
		}
	}

	// collect all template IDs needed
	tplIDSet := make(map[uint]struct{})
	for _, n := range nodeMap {
		if n.TemplateID > 0 {
			tplIDSet[n.TemplateID] = struct{}{}
		}
	}
	for _, tplID := range runMap {
		if tplID > 0 {
			tplIDSet[tplID] = struct{}{}
		}
	}
	tplMap := make(map[uint]string)
	if len(tplIDSet) > 0 {
		tplIDs := make([]uint, 0, len(tplIDSet))
		for id := range tplIDSet {
			tplIDs = append(tplIDs, id)
		}
		var tpls []struct {
			ID   uint
			Name string
		}
		if err := db.Model(&model.SopTemplate{}).
			Where("id IN ?", tplIDs).
			Select("id, name").
			Scan(&tpls).Error; err != nil {
			log.C(ctx).Warnw("enrichDetailNames: sop_template lookup failed", "err", err)
		}
		for _, t := range tpls {
			tplMap[t.ID] = t.Name
		}
	}

	// sales sessions: id → Title (scoped by user_id — 越权防护)
	salesMap := make(map[uint]string)
	if len(salesIDs) > 0 {
		var sessions []struct {
			ID    uint
			Title string
		}
		if err := db.Model(&model.SalesSession{}).
			Where("id IN ? AND user_id = ?", salesIDs, userID).
			Select("id, title").
			Scan(&sessions).Error; err != nil {
			log.C(ctx).Warnw("enrichDetailNames: sales_session lookup failed", "err", err)
		}
		for _, s := range sessions {
			salesMap[s.ID] = s.Title
		}
	}

	// chatbot sessions: id → ChatbotID
	cbSessMap := make(map[uint]uint)
	if len(cbSessIDs) > 0 {
		var sessions []struct {
			ID        uint
			ChatbotID uint
		}
		if err := db.Model(&model.ChatbotSession{}).
			Where("id IN ?", cbSessIDs).
			Select("id, chatbot_id").
			Scan(&sessions).Error; err != nil {
			log.C(ctx).Warnw("enrichDetailNames: chatbot_session lookup failed", "err", err)
		}
		for _, s := range sessions {
			cbSessMap[s.ID] = s.ChatbotID
		}
	}

	// chatbot configs: id → Name
	cbCfgMap := make(map[uint]string)
	if len(cbSessMap) > 0 {
		cfgIDSet := make(map[uint]struct{})
		for _, cfgID := range cbSessMap {
			if cfgID > 0 {
				cfgIDSet[cfgID] = struct{}{}
			}
		}
		if len(cfgIDSet) > 0 {
			cfgIDs := make([]uint, 0, len(cfgIDSet))
			for id := range cfgIDSet {
				cfgIDs = append(cfgIDs, id)
			}
			var cfgs []struct {
				ID   uint
				Name string
			}
			if err := db.Model(&model.ChatbotConfig{}).
				Where("id IN ?", cfgIDs).
				Select("id, name").
				Scan(&cfgs).Error; err != nil {
				log.C(ctx).Warnw("enrichDetailNames: chatbot_config lookup failed", "err", err)
			}
			for _, c := range cfgs {
				cbCfgMap[c.ID] = c.Name
			}
		}
	}

	// ── build result map ──────────────────────────────────────────────────────
	result := make(map[uint64]string, len(rows))
	for i, r := range rows {
		m := metas[i]
		switch m.prefix {
		case "sop_run":
			if m.nodeID == 0 || m.runID == 0 {
				break
			}
			node, ok := nodeMap[m.nodeID]
			if !ok {
				// node missing — try run → template only, no node name
				if tplID, ok2 := runMap[m.runID]; ok2 {
					result[r.ID] = tplMap[tplID]
				}
				break
			}
			tplName := tplMap[node.TemplateID]
			switch {
			case tplName != "" && node.Name != "":
				result[r.ID] = tplName + " · " + node.Name
			case node.Name != "":
				// template missing/soft-deleted but node exists — fall back to step name
				result[r.ID] = node.Name
			case tplName != "":
				result[r.ID] = tplName
			}
		case "sop_chat":
			if m.runID == 0 {
				break
			}
			if tplID, ok := runMap[m.runID]; ok {
				result[r.ID] = tplMap[tplID]
			}
		case "sales_session":
			if m.salesID > 0 {
				result[r.ID] = salesMap[m.salesID]
			}
		case "chatbot_session":
			if m.cbSessID == 0 {
				break
			}
			cfgID := cbSessMap[m.cbSessID]
			if cfgID > 0 {
				result[r.ID] = cbCfgMap[cfgID]
			}
		}
	}
	return result
}
