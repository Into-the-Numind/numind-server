package marketplace

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/biz/skill/artifact"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/model"
)

// cloneToSubscriber creates a Skill in the subscriber's tenant by copying the
// marketplace row's sanitized body + frontmatter. Wrapped in a Langfuse span
// "marketplace-subscribe-clone" when a TraceCtx is present (spec §9).
//
// This is Phase 1 of Subscribe's two-phase commit (S4-T4-D1). It commits the
// cloned skill independently; Subscribe's caller is responsible for invoking
// the compensating Delete if Phase 2 fails.
func (s *service) cloneToSubscriber(ctx context.Context, mp *model.SkillMarketplace, subscriberUserID uint) (uint, error) {
	tc := langfuse.FromContext(ctx)
	var spanID string
	if tc != nil {
		spanID = langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "marketplace-subscribe-clone",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]interface{}{
				"subscriber_user_id": subscriberUserID,
				"marketplace_id":     mp.ID,
				"source_skill_id":    mp.SourceSkillID,
			}),
		)
	}

	// Enrich description with provenance so the subscriber sees where it came from.
	enrichedDesc := fmt.Sprintf("%s\n\n[订阅自市场 / marketplace_id=%d / 订阅时间 %s]",
		mp.Description, mp.ID, time.Now().Format("2006-01-02"))

	createReq := artifact.CreateRequest{
		Name:         mp.Name,
		Description:  enrichedDesc,
		WhenToUse:    mp.WhenToUse,
		AllowedTools: allowedToolsFromJSON(mp.AllowedTools),
		BodyMd:       mp.SanitizedBodyMD,
		SourceType:   "imported_from_marketplace",
		// Note: artifact.CreateRequest.SourceType binding tag oneof does NOT include
		// "imported_from_marketplace" (spec investigation Q3 / S0-D2 revised). Binding
		// only fires on HTTP path; programmatic call bypasses it. DDL ENUM accepts.
	}
	cloned, err := s.artifactSvc.Create(ctx, subscriberUserID, subscriberUserID, createReq)

	if tc != nil && spanID != "" {
		if err != nil {
			langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanError(err.Error()))
		} else {
			langfuse.EndSpan(tc.TraceID, spanID,
				langfuse.WithSpanOutput(map[string]uint{"cloned_skill_id": cloned.ID}),
			)
		}
	}
	if err != nil {
		return 0, fmt.Errorf("cloneToSubscriber: %w", err)
	}
	return cloned.ID, nil
}

// unsubscribeCleanup is the inverse of cloneToSubscriber: soft-deletes the
// cloned skill in subscriber's tenant. Wrapped in a Langfuse span
// "marketplace-unsubscribe-cleanup" when a TraceCtx is present.
func (s *service) unsubscribeCleanup(ctx context.Context, subscriberUserID, clonedSkillID, marketplaceID uint) error {
	tc := langfuse.FromContext(ctx)
	var spanID string
	if tc != nil {
		spanID = langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "marketplace-unsubscribe-cleanup",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]interface{}{
				"subscriber_user_id": subscriberUserID,
				"cloned_skill_id":    clonedSkillID,
				"marketplace_id":     marketplaceID,
			}),
		)
	}

	_, err := s.artifactSvc.Delete(ctx, subscriberUserID, clonedSkillID)

	if tc != nil && spanID != "" {
		if err != nil {
			langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanError(err.Error()))
		} else {
			langfuse.EndSpan(tc.TraceID, spanID, langfuse.WithSpanOutput(map[string]bool{"soft_deleted": true}))
		}
	}
	if err != nil {
		return fmt.Errorf("unsubscribeCleanup: %w", err)
	}
	return nil
}
