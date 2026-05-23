package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// MigrationStats 记录一次迁移/回滚的统计信息（dry-run / 实际 / rollback）。
//
//   - Migrated     实际迁移的 active agent_definition 行数（非 dry-run 模式）
//   - WouldMigrate dry-run 模式下预计会迁移的行数
//   - Rolled       rollback 模式下删除的 binding 行数
type MigrationStats struct {
	Migrated     int
	WouldMigrate int
	Rolled       int
}

// migrationSkillNameSuffix 是迁移产物 skill 的命名后缀，用于 rollback 时定位迁移产物。
// 与 migrationSkillWhenToUse / migrationSkillVersion 一同构成 rollback 的四重过滤条件。
const (
	migrationSkillNameSuffix  = " 的默认技能"
	migrationSkillWhenToUse   = "从 v1 Agent 迁移派生，未指定使用场景"
	migrationSkillVersion     = 1
	defaultMigrationBatchSize = 100
)

// RunMigration 把 v1 active agent_definition 的内嵌 skill_body 迁移成 v2 skill 资产 +
// agent_skill_binding 装载关系（spec §2.1, ADR-15）。
//
// 核心算法：单事务内逐 agent 处理：
//  1. LEFT JOIN 找出 active 但未迁移的 agent_definition
//  2. 派生 skill（advanced_mode → custom_skill_body / 否则 → generated_skill_body）
//  3. 用 Go 变量传递 skill.ID 立即写 binding（避免 SQL JOIN 同秒 race）
//  4. 写 skill_history v1 快照
//  5. 最后 assert: active_agents == distinct(active binding.agent_id)
//
// 模式：
//   - dryRun=true：只跑 SELECT，不 INSERT；stats.WouldMigrate 记录预计数
//   - dryRun=false：实际写入；stats.Migrated 记录实际数
//
// 幂等：LEFT JOIN 排除已有 active binding 的 agent，重复跑只会处理新增 agent。
//
// batchSize<=0 时使用 defaultMigrationBatchSize。
func RunMigration(ctx context.Context, db *gorm.DB, dryRun bool, batchSize int) (*MigrationStats, error) {
	if batchSize <= 0 {
		batchSize = defaultMigrationBatchSize
	}
	stats := &MigrationStats{}

	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		offset := 0
		for {
			// 1. LEFT JOIN 取一批未迁移的 active agent
			var agents []model.AgentDefinition
			if err := tx.Raw(`
				SELECT ad.* FROM agent_definition ad
				LEFT JOIN agent_skill_binding b ON b.agent_id = ad.id AND b.is_active = 1
				WHERE ad.is_active = 1 AND b.id IS NULL
				ORDER BY ad.id LIMIT ? OFFSET ?
			`, batchSize, offset).Scan(&agents).Error; err != nil {
				return fmt.Errorf("RunMigration: scan agents at offset %d: %w", offset, err)
			}
			if len(agents) == 0 {
				break
			}

			for _, ad := range agents {
				if dryRun {
					stats.WouldMigrate++
					continue
				}

				// 2. 派生 skill body + source_type
				bodyMd := ad.GeneratedSkillBody
				sourceType := "generated"
				if ad.AdvancedMode {
					bodyMd = ad.CustomSkillBody
					sourceType = "custom"
				}

				// allowed_tools 复用 ad.ToolFlags（已是 datatypes.JSON）；
				// 若为空则给一个空数组以满足 not null 约束
				allowedTools := ad.ToolFlags
				if len(allowedTools) == 0 {
					allowedTools = datatypes.JSON([]byte("[]"))
				}

				skill := model.Skill{
					ParentUserID: ad.ParentUserID,
					Name:         ad.Name + migrationSkillNameSuffix,
					Description:  ad.Description,
					WhenToUse:    migrationSkillWhenToUse,
					AllowedTools: allowedTools,
					BodyMd:       bodyMd,
					SourceType:   sourceType,
					Version:      migrationSkillVersion,
					IsActive:     true,
					CreatedBy:    ad.CreatedBy,
					CreatedAt:    ad.CreatedAt,
					UpdatedAt:    ad.UpdatedAt,
				}
				// Select("*") 兜底 GORM bool default:true 陷阱（database.md §6）
				if err := tx.Select("*").Create(&skill).Error; err != nil {
					return fmt.Errorf("RunMigration: create skill for agent %d: %w", ad.ID, err)
				}

				// 3. 用 skill.ID 立即写 binding（ADR-15：避免 SQL JOIN race）
				binding := model.AgentSkillBinding{
					AgentID:   uint(ad.ID),
					SkillID:   skill.ID,
					SortOrder: 0,
					IsActive:  true,
					BoundAt:   ad.UpdatedAt,
				}
				if err := tx.Select("*").Create(&binding).Error; err != nil {
					return fmt.Errorf("RunMigration: create binding for agent %d: %w", ad.ID, err)
				}

				// 4. 写 history v1 快照
				snapshot, err := json.Marshal(skill)
				if err != nil {
					return fmt.Errorf("RunMigration: marshal snapshot for skill %d: %w", skill.ID, err)
				}
				history := model.SkillHistory{
					SkillID:   skill.ID,
					Version:   migrationSkillVersion,
					Snapshot:  datatypes.JSON(snapshot),
					CreatedBy: ad.CreatedBy,
					CreatedAt: ad.CreatedAt,
				}
				if err := tx.Create(&history).Error; err != nil {
					return fmt.Errorf("RunMigration: create history for skill %d: %w", skill.ID, err)
				}

				stats.Migrated++
			}

			// 实际迁移模式下，LEFT JOIN 会自动过滤掉已迁移的行（b.is_active=1 后变非 NULL），
			// 因此每次循环都从 offset=0 取下一批即可。
			// dry-run 模式不写入 binding，必须用 offset 推进否则会死循环。
			if dryRun {
				offset += len(agents)
			}
			if len(agents) < batchSize {
				break
			}
		}

		// 5. Assert（spec §2.1 step 5）：active agent 数应等于 active binding 的 distinct agent_id 数
		if !dryRun {
			var activeAgentCount, activeBindingAgentCount int64
			if err := tx.Model(&model.AgentDefinition{}).Where("is_active = ?", true).Count(&activeAgentCount).Error; err != nil {
				return fmt.Errorf("RunMigration: count active agents: %w", err)
			}
			if err := tx.Raw("SELECT COUNT(DISTINCT agent_id) FROM agent_skill_binding WHERE is_active = ?", true).Scan(&activeBindingAgentCount).Error; err != nil {
				return fmt.Errorf("RunMigration: count distinct active bindings: %w", err)
			}
			if activeAgentCount != activeBindingAgentCount {
				return fmt.Errorf("RunMigration: assert failed — active_agents=%d distinct_active_bindings=%d", activeAgentCount, activeBindingAgentCount)
			}
		}
		return nil
	})

	if err != nil {
		return stats, err
	}
	return stats, nil
}

// RunRollback 删除由 RunMigration 创建的 agent_skill_binding 行（保留 skill / history 行作审计）。
//
// 用四重过滤定位迁移产物，避免误删用户后续手动创建的同名 skill：
//  1. skill.source_type IN ('generated','custom')
//  2. skill.name LIKE '% 的默认技能'
//  3. skill.version = 1
//  4. 不存在 skill_history.version > 1（即从未被用户编辑过）
//
// 默认保留 skill 行（注释掉的可选删除语句作为安全网）。
func RunRollback(ctx context.Context, db *gorm.DB) (*MigrationStats, error) {
	stats := &MigrationStats{}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 找出 migrated 的 binding ID
		var bindingIDs []uint
		if err := tx.Raw(`
			SELECT b.id FROM agent_skill_binding b
			INNER JOIN skill s ON s.id = b.skill_id
			WHERE s.source_type IN ('generated','custom')
			  AND s.name LIKE ?
			  AND s.version = ?
			  AND NOT EXISTS (SELECT 1 FROM skill_history h WHERE h.skill_id = s.id AND h.version > ?)
		`, "%"+migrationSkillNameSuffix, migrationSkillVersion, migrationSkillVersion).Scan(&bindingIDs).Error; err != nil {
			return fmt.Errorf("RunRollback: locate migrated bindings: %w", err)
		}
		if len(bindingIDs) == 0 {
			return nil
		}

		// 2. 硬删 binding（迁移产物，安全删；skill 行保留作审计）
		res := tx.Where("id IN ?", bindingIDs).Delete(&model.AgentSkillBinding{})
		if res.Error != nil {
			return fmt.Errorf("RunRollback: delete bindings: %w", res.Error)
		}
		stats.Rolled = int(res.RowsAffected)

		// 3. 默认保留 skill 行（安全网；若需彻底清理，手动取消注释）：
		// tx.Where("source_type IN (?) AND name LIKE ? AND version = ?",
		//     []string{"generated", "custom"}, "%"+migrationSkillNameSuffix, migrationSkillVersion).
		//     Delete(&model.Skill{})

		return nil
	})
	if err != nil {
		return stats, err
	}
	return stats, nil
}

// newMigrateSkillFromAgentCmd 构造 `numind migrate-skill-from-agent` 子命令。
//
// 用法:
//
//	numind migrate-skill-from-agent              # 实际跑迁移
//	numind migrate-skill-from-agent --dry-run    # 只算预计数
//	numind migrate-skill-from-agent --rollback   # 删除迁移产物
//	numind migrate-skill-from-agent --batch-size 50
//
// 注：CLI 走 numind.NewNumindCommand() 同样的 initConfig + initStore 链路，
// 配置文件由 --config 持久标志指定（继承自父命令）。
func newMigrateSkillFromAgentCmd() *cobra.Command {
	var (
		dryRun    bool
		rollback  bool
		batchSize int
	)
	cmd := &cobra.Command{
		Use:   "migrate-skill-from-agent",
		Short: "迁移 v1 agent_definition 内嵌 skill_body 到 v2 skill 资产 + binding",
		Long: `把 v1 active agent_definition 的内嵌 skill_body 迁移成 v2 skill 资产 + agent_skill_binding 装载关系。

模式：
  --dry-run        只跑 SELECT 不 INSERT，输出预计迁移数
  --rollback       删除迁移产物（agent_skill_binding 硬删，skill 保留作审计）
  --batch-size N   每批处理 N 个 agent（默认 100）

幂等：重复执行只会处理新增的未迁移 agent。`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Init(numind.LogOptionsForCLI())
			defer log.Sync()

			db, err := numind.OpenDBForCLI()
			if err != nil {
				return fmt.Errorf("init db: %w", err)
			}
			ctx := cmd.Context()

			if rollback {
				stats, err := RunRollback(ctx, db)
				if err != nil {
					return err
				}
				fmt.Printf("[migrate-skill-from-agent] rollback OK: rolled_bindings=%d\n", stats.Rolled)
				return nil
			}

			stats, err := RunMigration(ctx, db, dryRun, batchSize)
			if err != nil {
				return err
			}
			if dryRun {
				fmt.Printf("[migrate-skill-from-agent] dry-run OK: would_migrate=%d\n", stats.WouldMigrate)
			} else {
				fmt.Printf("[migrate-skill-from-agent] migrate OK: migrated=%d\n", stats.Migrated)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只跑 SELECT 不 INSERT，输出预计迁移数")
	cmd.Flags().BoolVar(&rollback, "rollback", false, "删除迁移产物（binding 硬删，skill 保留）")
	cmd.Flags().IntVar(&batchSize, "batch-size", defaultMigrationBatchSize, "每批处理的 agent 数")

	return cmd
}
