package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/skill/artifact"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// Service is the marketplace business-orchestration layer (spec §3.1).
//
// All public methods FIRST verify the caller is a parent account (User.ParentUserID
// IS NULL). Child accounts → ErrChildAccountCannotAccessMarketplace.
//
// Cross-tenant isolation enforcement (spec §10.1, 7 rules):
//  1. publisher/subscriber IDs are taken from the JWT context via the caller, never
//     from request body (controller signature ensures this).
//  2. Child account blocked on every method.
//  3. Publish: skill not owned by publisher → ErrSkillNotOwned.
//  4. Subscribe: self-subscribe → ErrSelfSubscribeForbidden.
//  5. Unsubscribe: subscription not owned by caller → not-found.
//  6. Get: unpublished item visible to publisher only.
//  7. Subscribe writes cloned skill with parent_user_id=subscriberUserID via
//     artifactSvc.Create (#1 enforces parent_user_id strictly).
type Service interface {
	SanitizePreview(ctx context.Context, publisherUserID, skillID uint) (sanitizedBodyMD string, err error)
	Publish(ctx context.Context, publisherUserID uint, req PublishRequest) (*model.SkillMarketplace, error)
	Unpublish(ctx context.Context, publisherUserID, marketplaceID uint) error
	List(ctx context.Context, query BrowseQuery) (items []*model.SkillMarketplace, total int64, err error)
	Get(ctx context.Context, marketplaceID, callerUserID uint) (*model.SkillMarketplace, error)
	Subscribe(ctx context.Context, subscriberUserID, marketplaceID uint) (clonedSkillID uint, err error)
	Unsubscribe(ctx context.Context, subscriberUserID, marketplaceID uint) error
	ListMySubscriptions(ctx context.Context, subscriberUserID uint, offset, limit int) ([]SubscriptionItem, int64, error)
	SetRecommended(ctx context.Context, marketplaceID uint, recommended bool) error
}

// SubscriptionItem is the biz-layer hydrated subscription row returned to controllers.
type SubscriptionItem struct {
	Subscription model.SkillSubscription `json:"subscription"`
	Marketplace  model.SkillMarketplace  `json:"marketplace"`
	AgentCount   int                     `json:"agent_count"`
}

// ArtifactService is the subset of skill/artifact.Service that marketplace uses.
// Abstracted to an interface so unit tests can mock it (artifact.Service is a
// concrete struct from #1's package and cannot be mocked directly). Production
// wiring passes *artifact.Service which satisfies this interface.
type ArtifactService interface {
	Create(ctx context.Context, parentUserID, createdBy uint, req artifact.CreateRequest) (*model.Skill, error)
	Get(ctx context.Context, parentUserID, skillID uint) (*model.Skill, error)
	Delete(ctx context.Context, parentUserID, skillID uint) (int64, error)
}

// Sentinel errors (T7 will replace with internal/pkg/errno/skill_marketplace.go
// &Errno{HTTP, Code, Message} struct literals per spec §4.3 revised).
var (
	ErrChildAccountCannotAccessMarketplace = errors.New("marketplace: child account forbidden")
	ErrSkillNotOwned                       = errors.New("marketplace: skill not owned by caller")
	ErrSkillAlreadyPublished               = errors.New("marketplace: skill already published")
	ErrSelfSubscribeForbidden              = errors.New("marketplace: cannot subscribe to your own publication")
	ErrAlreadySubscribed                   = errors.New("marketplace: already subscribed")
	ErrMarketplaceNotFound                 = errors.New("marketplace: item not found")
	ErrSubscriptionNotFound                = errors.New("marketplace: subscription not found")
	ErrSanitizeConfirmationMismatch        = errors.New("marketplace: confirmed_sanitized_body differs substantially from re-run sanitize output")
	ErrSkillBodyEmpty                      = errors.New("marketplace: skill body is empty")
)

// confirmationDeltaTolerance (S2-D2): publisher's frontend echoes back what they
// reviewed in the diff; the biz layer re-runs sanitize and compares. If character
// count differs by >this fraction, assume tampering. Empirically LLM output has
// ~1-2% non-determinism, 5% gives headroom.
const confirmationDeltaTolerance = 0.05

type service struct {
	store       store.IMarketplaceStore
	artifactSvc ArtifactService
	userStore   store.UserStore
	db          *gorm.DB
}

// NewService constructs the marketplace orchestration service.
func NewService(s store.IMarketplaceStore, art ArtifactService, users store.UserStore, db *gorm.DB) Service {
	return &service{store: s, artifactSvc: art, userStore: users, db: db}
}

// ---- helpers ----

// verifyParent ensures the user is a parent account (parent_user_id IS NULL).
// Returns ErrChildAccountCannotAccessMarketplace if it's a child.
func (s *service) verifyParent(ctx context.Context, userID uint) error {
	u, err := s.userStore.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("verifyParent: lookup user %d: %w", userID, err)
	}
	if u.ParentUserID != nil {
		return ErrChildAccountCannotAccessMarketplace
	}
	return nil
}

// allowedToolsAsJSON serializes a []string to datatypes.JSON. Nil-safe (empty array).
func allowedToolsAsJSON(tools []string) datatypes.JSON {
	if tools == nil {
		tools = []string{}
	}
	b, _ := json.Marshal(tools)
	return datatypes.JSON(b)
}

// allowedToolsFromJSON parses datatypes.JSON to []string. Tolerates nil/malformed.
func allowedToolsFromJSON(j datatypes.JSON) []string {
	if len(j) == 0 {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal(j, &out); err != nil {
		return []string{}
	}
	return out
}

// normalizeForCompare collapses whitespace + lowercases for a tolerant
// publisher-echoed-body match. Doesn't strip markdown (we want to detect
// content tampering, not formatting drift).
func normalizeForCompare(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// charDelta returns the absolute character-count difference as a fraction of
// max(len(a), len(b)). 0 means identical length; 1 means one is empty.
func charDelta(a, b string) float64 {
	la, lb := len(a), len(b)
	if la == 0 && lb == 0 {
		return 0
	}
	diff := la - lb
	if diff < 0 {
		diff = -diff
	}
	max := la
	if lb > max {
		max = lb
	}
	return float64(diff) / float64(max)
}

// ---- Langfuse trace helper ----

// startMarketplaceTrace creates a Langfuse trace tagged "agent-mode-v2",
// "marketplace" plus the operation name. Returns the ctx carrying the trace ID
// and a finalize fn the caller should defer with `&err` (named return).
//
// finalize records the operation outcome via UpdateTraceMetadata when an error
// is present; on success it's a no-op (downstream generations/spans already
// describe the work). Safe when Langfuse is disabled (CreateTrace early-returns).
func startMarketplaceTrace(ctx context.Context, name string, userID uint, input map[string]interface{}) (context.Context, func(perr *error)) {
	traceID := langfuse.TraceID()
	langfuse.CreateTrace(traceID, name,
		langfuse.WithUserID(userID),
		langfuse.WithTraceInput(input),
		langfuse.WithTraceTags("agent-mode-v2", "marketplace"),
	)
	ctx = langfuse.WithTrace(ctx, traceID)
	finalize := func(perr *error) {
		if perr == nil || *perr == nil {
			return
		}
		langfuse.UpdateTraceMetadata(traceID, map[string]string{
			"error": (*perr).Error(),
		})
	}
	return ctx, finalize
}

// ---- public methods ----

func (s *service) SanitizePreview(ctx context.Context, publisherUserID, skillID uint) (sanitizedBodyMD string, err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-sanitize-preview", publisherUserID, map[string]interface{}{
		"publisher_user_id": publisherUserID,
		"skill_id":          skillID,
	})
	defer finalize(&err)

	if err = s.verifyParent(ctx, publisherUserID); err != nil {
		return "", err
	}
	sk, err := s.artifactSvc.Get(ctx, publisherUserID, skillID)
	if err != nil {
		return "", fmt.Errorf("SanitizePreview: get skill: %w", err)
	}
	if sk.BodyMd == "" {
		return "", ErrSkillBodyEmpty
	}
	res, err := Sanitize(ctx, sk.BodyMd)
	if err != nil {
		return "", err
	}
	return res.SanitizedBodyMD, nil
}

func (s *service) Publish(ctx context.Context, publisherUserID uint, req PublishRequest) (mp *model.SkillMarketplace, err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-publish", publisherUserID, map[string]interface{}{
		"publisher_user_id": publisherUserID,
		"skill_id":          req.SkillID,
		"category_tags":     req.CategoryTags,
	})
	defer finalize(&err)

	if err = s.verifyParent(ctx, publisherUserID); err != nil {
		return nil, err
	}
	sk, err := s.artifactSvc.Get(ctx, publisherUserID, req.SkillID)
	if err != nil {
		return nil, fmt.Errorf("Publish: get skill: %w", err)
	}
	// artifactSvc.Get already filters by parent_user_id, so owner check is
	// implicit; defensive double-check for cross-tenant rule 3.
	if sk.ParentUserID != publisherUserID {
		return nil, ErrSkillNotOwned
	}
	if !sk.IsActive {
		return nil, ErrSkillNotOwned
	}
	if sk.BodyMd == "" {
		return nil, ErrSkillBodyEmpty
	}

	// Uniqueness pre-check (S2-D1): one active marketplace row per source_skill_id.
	if existing, err := s.store.GetActiveBySourceSkillID(ctx, req.SkillID); err == nil && existing != nil {
		return nil, ErrSkillAlreadyPublished
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("Publish: uniqueness check: %w", err)
	}

	// Re-run sanitize internally (S2-D2: don't trust frontend echo blindly).
	internal, err := Sanitize(ctx, sk.BodyMd)
	if err != nil {
		return nil, err
	}

	// Compare normalized versions, allow up to confirmationDeltaTolerance drift.
	normInternal := normalizeForCompare(internal.SanitizedBodyMD)
	normConfirmed := normalizeForCompare(req.ConfirmedSanitizedBodyMD)
	if normInternal != normConfirmed {
		if charDelta(normInternal, normConfirmed) > confirmationDeltaTolerance {
			return nil, ErrSanitizeConfirmationMismatch
		}
	}

	mp = &model.SkillMarketplace{
		PublisherUserID: publisherUserID,
		SourceSkillID:   req.SkillID,
		Name:            sk.Name,
		Description:     sk.Description,
		WhenToUse:       sk.WhenToUse,
		SanitizedBodyMD: internal.SanitizedBodyMD,
		AllowedTools:    sk.AllowedTools,
		CategoryTags:    allowedToolsAsJSON(req.CategoryTags), // re-uses helper for []string→JSON
		IsPublic:        true,
	}
	if err = s.store.Create(ctx, mp); err != nil {
		return nil, fmt.Errorf("Publish: store create: %w", err)
	}
	return mp, nil
}

func (s *service) Unpublish(ctx context.Context, publisherUserID, marketplaceID uint) (err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-unpublish", publisherUserID, map[string]interface{}{
		"publisher_user_id": publisherUserID,
		"marketplace_id":    marketplaceID,
	})
	defer finalize(&err)

	if err = s.verifyParent(ctx, publisherUserID); err != nil {
		return err
	}
	mp, err := s.store.GetByID(ctx, marketplaceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrMarketplaceNotFound
		}
		return fmt.Errorf("Unpublish: get: %w", err)
	}
	if mp.PublisherUserID != publisherUserID {
		return ErrSkillNotOwned
	}
	if err = s.store.UpdateIsPublic(ctx, marketplaceID, false); err != nil {
		return fmt.Errorf("Unpublish: update: %w", err)
	}
	return nil
}

func (s *service) List(ctx context.Context, query BrowseQuery) ([]*model.SkillMarketplace, int64, error) {
	if query.PageSize <= 0 {
		query.PageSize = 20
	}
	if query.PageSize > 100 {
		query.PageSize = 100
	}
	if query.Page <= 0 {
		query.Page = 1
	}
	opts := store.ListOptions{
		// Escape Q for MySQL FULLTEXT BOOLEAN MODE (search.go.booleanModeQuery) —
		// strips operators (+, -, *, etc.) and prefix-AND's surviving tokens.
		Q:        booleanModeQuery(query.Q),
		Category: query.Category,
		Sort:     query.Sort,
		Offset:   (query.Page - 1) * query.PageSize,
		Limit:    query.PageSize,
	}
	return s.store.List(ctx, opts)
}

func (s *service) Get(ctx context.Context, marketplaceID, callerUserID uint) (*model.SkillMarketplace, error) {
	mp, err := s.store.GetByID(ctx, marketplaceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMarketplaceNotFound
		}
		return nil, fmt.Errorf("Get: %w", err)
	}
	// Rule 6: unpublished is visible only to publisher.
	if !mp.IsPublic && mp.PublisherUserID != callerUserID {
		return nil, ErrMarketplaceNotFound
	}
	return mp, nil
}

// Subscribe is the critical cross-tenant clone path.
//
// Two-phase commit (S4-T4-D1): #1's artifact.Service.Create opens its own
// db.Transaction internally. Because GORM v2's nested-tx detection is
// per-*gorm.DB-instance and we cannot pass our tx into artifact.Service
// without modifying #1, the spec §11 / S2-D3 "nested savepoint" approach
// does NOT actually save-point — the inner Create commits independently.
//
// Therefore: (1) call artifactSvc.Create OUTSIDE our tx (commits the cloned
// skill); (2) open our tx for skill_subscription insert + subscribe_count
// increment; (3) on tx failure, compensate by calling artifactSvc.Delete
// to soft-remove the orphan cloned skill. Compensation failures are logged
// but don't override the original error — the orphan is a soft-deleted skill
// (is_active=0) that doesn't bind to any agent, so the customer-facing impact
// is minimal.
func (s *service) Subscribe(ctx context.Context, subscriberUserID, marketplaceID uint) (clonedSkillID uint, err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-subscribe", subscriberUserID, map[string]interface{}{
		"subscriber_user_id": subscriberUserID,
		"marketplace_id":     marketplaceID,
	})
	defer finalize(&err)

	if err = s.verifyParent(ctx, subscriberUserID); err != nil {
		return 0, err
	}
	mp, err := s.store.GetByID(ctx, marketplaceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrMarketplaceNotFound
		}
		return 0, fmt.Errorf("Subscribe: get marketplace: %w", err)
	}
	if !mp.IsPublic {
		return 0, ErrMarketplaceNotFound
	}
	if mp.PublisherUserID == subscriberUserID {
		return 0, ErrSelfSubscribeForbidden
	}

	// UNIQUE pre-check (race-tolerant; final UNIQUE constraint at DB is the
	// authoritative guard).
	if _, subErr := s.store.GetSubscription(ctx, subscriberUserID, marketplaceID); subErr == nil {
		return 0, ErrAlreadySubscribed
	} else if !errors.Is(subErr, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("Subscribe: pre-check existing: %w", subErr)
	}

	// Phase 1: clone skill in subscriber's tenant (wrapped in Langfuse span by clone.go).
	clonedID, err := s.cloneToSubscriber(ctx, mp, subscriberUserID)
	if err != nil {
		return 0, fmt.Errorf("Subscribe: %w", err)
	}

	// Phase 2: tx for subscription + subscribe_count.
	sub := &model.SkillSubscription{
		SubscriberUserID: subscriberUserID,
		MarketplaceID:    marketplaceID,
		ClonedSkillID:    clonedID,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.store.CreateSubscription(ctx, tx, sub); e != nil {
			return e
		}
		return s.store.IncrementSubscribeCount(ctx, tx, marketplaceID, +1)
	})
	if err != nil {
		// Compensate phase 1 — best-effort soft-delete of cloned skill.
		if _, delErr := s.artifactSvc.Delete(ctx, subscriberUserID, clonedID); delErr != nil {
			log.Errorw("Subscribe: orphan cloned_skill compensation failed",
				"subscriber_user_id", subscriberUserID,
				"marketplace_id", marketplaceID,
				"cloned_skill_id", clonedID,
				"original_error", err.Error(),
				"compensation_error", delErr.Error(),
			)
		}
		return 0, fmt.Errorf("Subscribe: phase2 tx: %w", err)
	}
	return clonedID, nil
}

// Unsubscribe deletes the subscription + soft-deletes the cloned skill.
// Same two-phase concern as Subscribe: artifactSvc.Delete commits independently.
// On phase 2 failure (subscription delete + count decrement) we log + return;
// the cloned skill is already soft-deleted (low-impact orphan: subscriber sees
// no UI for it).
func (s *service) Unsubscribe(ctx context.Context, subscriberUserID, marketplaceID uint) (err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-unsubscribe", subscriberUserID, map[string]interface{}{
		"subscriber_user_id": subscriberUserID,
		"marketplace_id":     marketplaceID,
	})
	defer finalize(&err)

	if err = s.verifyParent(ctx, subscriberUserID); err != nil {
		return err
	}
	sub, err := s.store.GetSubscription(ctx, subscriberUserID, marketplaceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSubscriptionNotFound
		}
		return fmt.Errorf("Unsubscribe: get sub: %w", err)
	}

	// Phase 1: soft-delete cloned skill (wrapped in Langfuse span by clone.go).
	if err = s.unsubscribeCleanup(ctx, subscriberUserID, sub.ClonedSkillID, marketplaceID); err != nil {
		return err
	}

	// Phase 2: delete subscription + decrement count.
	return s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.store.DeleteSubscription(ctx, tx, subscriberUserID, marketplaceID); e != nil {
			return fmt.Errorf("Unsubscribe: store delete: %w", e)
		}
		if e := s.store.IncrementSubscribeCount(ctx, tx, marketplaceID, -1); e != nil {
			return fmt.Errorf("Unsubscribe: decrement: %w", e)
		}
		return nil
	})
}

func (s *service) ListMySubscriptions(ctx context.Context, subscriberUserID uint, offset, limit int) (items []SubscriptionItem, total int64, err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-list-subscriptions", subscriberUserID, map[string]interface{}{
		"subscriber_user_id": subscriberUserID,
		"offset":             offset,
		"limit":              limit,
	})
	defer finalize(&err)

	if err = s.verifyParent(ctx, subscriberUserID); err != nil {
		return nil, 0, err
	}
	rows, total, err := s.store.ListMySubscriptions(ctx, subscriberUserID, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("ListMySubscriptions: store: %w", err)
	}
	if len(rows) == 0 {
		return []SubscriptionItem{}, total, nil
	}

	// Hydrate AgentCount per cloned_skill (S4-T2-D1).
	clonedIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		clonedIDs = append(clonedIDs, r.Subscription.ClonedSkillID)
	}
	counts := s.countActiveBindingsBySkillIDs(ctx, clonedIDs)

	out := make([]SubscriptionItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, SubscriptionItem{
			Subscription: r.Subscription,
			Marketplace:  r.Marketplace,
			AgentCount:   counts[r.Subscription.ClonedSkillID],
		})
	}
	return out, total, nil
}

// countActiveBindingsBySkillIDs returns a map of skill_id → active binding count
// for the given skill IDs. Direct GORM query rather than an IBindingStore method
// to avoid touching #1's package surface.
func (s *service) countActiveBindingsBySkillIDs(ctx context.Context, skillIDs []uint) map[uint]int {
	if len(skillIDs) == 0 {
		return map[uint]int{}
	}
	type row struct {
		SkillID uint  `gorm:"column:skill_id"`
		Cnt     int64 `gorm:"column:cnt"`
	}
	var rows []row
	err := s.db.WithContext(ctx).
		Model(&model.AgentSkillBinding{}).
		Select("skill_id, COUNT(*) AS cnt").
		Where("skill_id IN ? AND is_active = ?", skillIDs, true).
		Group("skill_id").
		Scan(&rows).Error
	if err != nil {
		log.Warnw("marketplace: countActiveBindingsBySkillIDs failed",
			"err", err.Error(),
			"skill_ids", skillIDs,
		)
		return map[uint]int{}
	}
	out := make(map[uint]int, len(rows))
	for _, r := range rows {
		out[r.SkillID] = int(r.Cnt)
	}
	return out
}

// SetRecommended is defined in admin.go.
