package artifact

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// historyDiffMaxLen 是 diff_summary 截断阈值（spec §3.2 「截断到 200 字符」）。
const historyDiffMaxLen = 200

// ListHistory 列出指定 skill 的所有历史版本（含 diff_summary）。
//
// 流程：
//  1. 子账户兜底 + 校验 skill 所有权（Get 含 is_active=0 →
//     即使 skill 已软删，历史仍可访问，与 v1 行为一致）
//  2. 拉所有 history 记录（按 version DESC）
//  3. 每条记录对比 *前一个版本*（version-1 行）算 diff_summary
//
// diff 算法：
//   - 解析 snapshot JSON → model.Skill
//   - 比对 name / description / when_to_use / allowed_tools / body_md 5 个字段
//   - body_md 用 sergi/go-diff 算行级 diff（+N -M）
//   - 其他字段只看「是否变」（变了就列字段名）
//   - 输出格式："修改了 body_md / description（+12 行 -3 行）"，>200 字符截断
//
// v1（最早的版本）无前置版本，diff_summary = "首次发布"。
func (v *Versioning) ListHistory(ctx context.Context, parentUserID, skillID uint) ([]HistoryItem, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}

	// 校验所有权
	if _, err := v.skillStore.Get(ctx, parentUserID, skillID); err != nil {
		return nil, err
	}

	histories, err := v.historyStore.ListBySkill(ctx, skillID)
	if err != nil {
		return nil, err
	}

	// 倒序遍历 histories（已按 version DESC 排序），为每条算 diff。
	// 上一个版本 = histories[i+1]（version 较低的那条）。
	items := make([]HistoryItem, 0, len(histories))
	for i, h := range histories {
		var summary string
		if i == len(histories)-1 {
			// 最早的版本（version=1 一般）
			summary = "首次发布"
		} else {
			prev := histories[i+1]
			summary = computeDiffSummary(prev.Snapshot, h.Snapshot)
		}
		items = append(items, HistoryItem{
			Version:     h.Version,
			CreatedAt:   h.CreatedAt,
			CreatedBy:   h.CreatedBy,
			DiffSummary: summary,
		})
	}
	return items, nil
}

// Restore 从指定历史版本快照恢复 skill，并写入新版本 history。
//
// 流程：
//  1. 子账户兜底 + 校验 skill 所有权
//  2. 取目标版本 history 快照
//  3. 解析 snapshot → 应用到当前 skill（保留 ID / ParentUserID / CreatedBy / CreatedAt）
//  4. version = current_max_version + 1
//  5. 若当前 skill is_active=0 → 同时复活 is_active=1（spec §3.2 「Restore 复活软删 skill」）
//  6. 事务内：Update skill + 写 history 新行
//
// 不存在的版本 → ErrSkillArtifactVersionNotFound。
// 历史里旧的版本不删（append-only）。
func (v *Versioning) Restore(ctx context.Context, parentUserID, skillID, version uint) (*model.Skill, error) {
	if parentUserID == 0 {
		return nil, errno.ErrPermissionDenied
	}

	sk, err := v.skillStore.Get(ctx, parentUserID, skillID)
	if err != nil {
		return nil, err
	}

	hist, err := v.historyStore.GetByVersion(ctx, skillID, version)
	if err != nil {
		return nil, err
	}

	// 把目标版本快照解析回 model.Skill；只取业务字段，不覆盖 identity。
	var snap model.Skill
	if err := json.Unmarshal(hist.Snapshot, &snap); err != nil {
		return nil, fmt.Errorf("Restore unmarshal snapshot: %w", err)
	}

	// 计算当前最大版本号（从 history 取，因为 skill.version 应当等于最大；但用 history 更鲁棒）
	maxVer, err := v.maxVersion(ctx, skillID)
	if err != nil {
		return nil, err
	}

	// 覆盖业务字段，保留 identity
	sk.Name = snap.Name
	sk.Description = snap.Description
	sk.WhenToUse = snap.WhenToUse
	sk.AllowedTools = snap.AllowedTools
	sk.BodyMd = snap.BodyMd
	sk.SourceType = snap.SourceType
	sk.SourceTemplateID = snap.SourceTemplateID
	sk.Version = maxVer + 1
	sk.IsActive = true // 复活（即使原本是 is_active=0）

	err = v.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := v.skillStore.UpdateTx(ctx, tx, sk); err != nil {
			return err
		}
		return v.writeSnapshotTx(ctx, tx, sk, parentUserID)
	})
	if err != nil {
		return nil, err
	}
	return sk, nil
}

// maxVersion 返回该 skill 历史中最大版本号；无历史返回 0。
func (v *Versioning) maxVersion(ctx context.Context, skillID uint) (uint, error) {
	histories, err := v.historyStore.ListBySkill(ctx, skillID)
	if err != nil {
		return 0, err
	}
	var max uint
	for _, h := range histories {
		if h.Version > max {
			max = h.Version
		}
	}
	return max, nil
}

// computeDiffSummary 比较两份 skill JSON snapshot，输出可读 diff（spec §3.2 范例：
// "修改了 body_md（+12 行 -3 行）"）。
//
// 解析失败时返回 "更新"，避免阻塞 ListHistory。
func computeDiffSummary(prev, curr datatypes.JSON) string {
	var p, c model.Skill
	if err := json.Unmarshal(prev, &p); err != nil {
		return "更新"
	}
	if err := json.Unmarshal(curr, &c); err != nil {
		return "更新"
	}

	var changed []string
	if p.Name != c.Name {
		changed = append(changed, "name")
	}
	if p.Description != c.Description {
		changed = append(changed, "description")
	}
	if p.WhenToUse != c.WhenToUse {
		changed = append(changed, "when_to_use")
	}
	if string(p.AllowedTools) != string(c.AllowedTools) {
		changed = append(changed, "allowed_tools")
	}
	if p.SourceType != c.SourceType {
		changed = append(changed, "source_type")
	}
	// is_active 变化（软删→复活、或反过来）
	if p.IsActive != c.IsActive {
		if c.IsActive {
			changed = append(changed, "复活")
		} else {
			changed = append(changed, "软删除")
		}
	}

	bodyChanged := p.BodyMd != c.BodyMd
	if bodyChanged {
		changed = append(changed, "body_md")
	}

	if len(changed) == 0 {
		return "更新"
	}

	summary := "修改了 " + strings.Join(changed, " / ")
	if bodyChanged {
		plus, minus := lineDiffCount(p.BodyMd, c.BodyMd)
		summary += fmt.Sprintf("（+%d 行 -%d 行）", plus, minus)
	}

	if len(summary) > historyDiffMaxLen {
		// 按 rune 截断防止断开 UTF-8 字符
		runes := []rune(summary)
		if len(runes) > historyDiffMaxLen {
			summary = string(runes[:historyDiffMaxLen-3]) + "..."
		}
	}
	return summary
}

// lineDiffCount 计算 body 从 a → b 的行级新增 / 删除数。
//
// 用 sergi/go-diff/diffmatchpatch：先把 a 和 b 按行 hash 成 rune（DiffLinesToRunes），
// 再做经典 Myers diff（DiffMainRunes），按 op 累计 insert / delete 数。
//
// 这套算法比"逐行比较两段字符串"更精确（能识别中间行插入而不是全段重写）。
func lineDiffCount(a, b string) (insert, del int) {
	dmp := diffmatchpatch.New()
	// runes is a slice with 3 elements: chars1, chars2, lineArray
	chars1, chars2, _ := dmp.DiffLinesToRunes(a, b)
	diffs := dmp.DiffMainRunes(chars1, chars2, false)
	for _, d := range diffs {
		// 每个 rune 代表 a/b 中的一行
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			insert += len([]rune(d.Text))
		case diffmatchpatch.DiffDelete:
			del += len([]rune(d.Text))
		}
	}
	return insert, del
}
