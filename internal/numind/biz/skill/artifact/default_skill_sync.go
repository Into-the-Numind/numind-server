package artifact

import (
	"context"
	"encoding/json"
	"strings"

	"gorm.io/gorm"
)

// migrationDefaultSkillSuffix is the name suffix migrate-skill-from-agent gives the
// skill it creates from an agent's embedded body ("<agent> 的默认技能"). Kept in sync
// with cmd/numind/migrate_skill_artifact.go::migrationSkillNameSuffix.
const migrationDefaultSkillSuffix = " 的默认技能"

// DefaultSkillSyncer writes a v1 questionnaire edit's rebuilt body through to the
// agent's bound migration-created "默认技能", so the runtime-loaded skill body never
// goes stale after an edit. It implements skill.DefaultSkillSyncer and is injected
// into the v1 skill.Service via WithDefaultSkillSyncer (dependency inversion avoids the
// import cycle — artifact already depends on skill for skill.Build).
type DefaultSkillSyncer struct {
	binding *BindingService
	svc     *Service
}

// NewDefaultSkillSyncer constructs the syncer over the shared *gorm.DB.
func NewDefaultSkillSyncer(db *gorm.DB) *DefaultSkillSyncer {
	return &DefaultSkillSyncer{
		binding: NewBindingService(db),
		svc:     NewService(db),
	}
}

// SyncAgentDefaultSkill finds the agent's bound default skill (name suffix match) and
// updates its body to newBody via Service.Update (bumping version + writing history).
// It is a no-op when the agent has no such skill (the common, un-migrated case) and
// when the body is already in sync. Only the FIRST suffix match is synced — the dup
// guard (Attach) keeps that unambiguous.
func (d *DefaultSkillSyncer) SyncAgentDefaultSkill(ctx context.Context, parentUserID, agentID uint, newBody string) error {
	skills, err := d.binding.ListByAgent(ctx, parentUserID, agentID)
	if err != nil {
		return err
	}
	for i := range skills {
		sk := &skills[i]
		if !strings.HasSuffix(sk.Name, migrationDefaultSkillSuffix) {
			continue
		}
		if sk.BodyMd == newBody {
			return nil // already in sync — avoid a needless version bump
		}
		var allowed []string
		if len(sk.AllowedTools) > 0 {
			_ = json.Unmarshal(sk.AllowedTools, &allowed)
		}
		// SourceType "" preserves the skill's existing source/origin (Service.Update
		// only overwrites them when a non-empty SourceType is supplied).
		_, uerr := d.svc.Update(ctx, parentUserID, sk.ID, CreateRequest{
			Name:             sk.Name,
			Description:      sk.Description,
			WhenToUse:        sk.WhenToUse,
			AllowedTools:     allowed,
			BodyMd:           newBody,
			SourceTemplateID: sk.SourceTemplateID, // preserve (Update clobbers it unconditionally)
		})
		return uerr
	}
	return nil
}
