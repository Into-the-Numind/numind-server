package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/skill/artifact"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
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
	// SanitizePreview returns the full sanitize result (body + stages + token usage)
	// so the frontend diff view can show debug info if desired (spec §4.1 sanitize-preview response).
	SanitizePreview(ctx context.Context, publisherUserID, skillID uint) (*SanitizeResult, error)
	Publish(ctx context.Context, publisherUserID uint, req PublishRequest) (*model.SkillMarketplace, error)
	Unpublish(ctx context.Context, publisherUserID, marketplaceID uint) error
	List(ctx context.Context, callerUserID uint, query BrowseQuery) (items []*model.SkillMarketplace, total int64, err error)
	Get(ctx context.Context, marketplaceID, callerUserID uint) (*model.SkillMarketplace, error)
	// Subscribe (T4 reference-mode) returns the subscription row ID and the
	// publisher's source_skill_id (spec T4 §E subscribe response:
	// {subscription_id, source_skill_id}). cloned_skill_id is no longer returned.
	Subscribe(ctx context.Context, subscriberUserID, marketplaceID uint) (subscriptionID, sourceSkillID uint, err error)
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
//
// T4: Subscribe no longer clones via Create (reference-mode writes the pointer
// row directly via s.db). Create stays in the interface only for the LEGACY
// clone.go path (cloneToSubscriber), which is no longer invoked by the new
// reference Subscribe. Get is used by Publish; DeleteInternal soft-deletes a
// reference-pointer (or legacy clone) skill row by (parentUserID, skillID).
type ArtifactService interface {
	Create(ctx context.Context, callerUserID, instID uint, isParent bool, req artifact.CreateRequest) (*model.Skill, error)
	Get(ctx context.Context, parentUserID, skillID uint) (*model.Skill, error)
	DeleteInternal(ctx context.Context, parentUserID, skillID uint) (int64, error)
}

// Error aliases — point to canonical errno package values (T7). Each alias is
// the SAME *Errno pointer; existing callers (`errors.Is(err, biz.ErrXxx)`) still
// match because errno.Errno doesn't implement Is() and errors.Is falls back to
// pointer identity. core.WriteResponse → errno.Decode unwraps the chain and
// reads HTTP / Code / Message from the *Errno.
var (
	ErrChildAccountCannotAccessMarketplace error = errno.ErrChildAccountCannotAccessMarketplace
	ErrSkillNotOwned                       error = errno.ErrSkillNotOwned
	ErrSkillAlreadyPublished               error = errno.ErrSkillAlreadyPublished
	ErrSelfSubscribeForbidden              error = errno.ErrSelfSubscribeForbidden
	ErrAlreadySubscribed                   error = errno.ErrAlreadySubscribed
	ErrMarketplaceNotFound                 error = errno.ErrMarketplaceNotFound
	ErrSubscriptionNotFound                error = errno.ErrSubscriptionNotFound
	ErrSanitizeConfirmationMismatch        error = errno.ErrSanitizeConfirmationMismatch
	ErrSkillBodyEmpty                      error = errno.ErrSkillBodyEmpty
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

// resolveCaller resolves the caller's institution id + parent/child role
// (T4 dual-level publish). instID = caller.ParentUserID==nil ? caller.ID : *caller.ParentUserID.
func (s *service) resolveCaller(ctx context.Context, callerUserID uint) (instID uint, isParent bool, err error) {
	u, err := s.userStore.GetByID(ctx, callerUserID)
	if err != nil {
		return 0, false, fmt.Errorf("resolveCaller: lookup user %d: %w", callerUserID, err)
	}
	if u.ParentUserID == nil {
		return u.ID, true, nil
	}
	return *u.ParentUserID, false, nil
}

// getOwnedSkillForPublish reads a skill by id scoped to the caller's institution
// (parent_user_id = instID). Missing / cross-institution → ErrSkillNotOwned (no
// existence reveal). The publisher NEVER reads another tenant's skill table.
func (s *service) getOwnedSkillForPublish(ctx context.Context, instID, skillID uint) (*model.Skill, error) {
	var sk model.Skill
	err := s.db.WithContext(ctx).
		Where("id = ? AND parent_user_id = ?", skillID, instID).
		First(&sk).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSkillNotOwned
		}
		return nil, fmt.Errorf("getOwnedSkillForPublish: %w", err)
	}
	return &sk, nil
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

func (s *service) SanitizePreview(ctx context.Context, publisherUserID, skillID uint) (res *SanitizeResult, err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-sanitize-preview", publisherUserID, map[string]interface{}{
		"publisher_user_id": publisherUserID,
		"skill_id":          skillID,
	})
	defer finalize(&err)

	if err = s.verifyParent(ctx, publisherUserID); err != nil {
		return nil, err
	}
	sk, err := s.artifactSvc.Get(ctx, publisherUserID, skillID)
	if err != nil {
		return nil, fmt.Errorf("SanitizePreview: get skill: %w", err)
	}
	if sk.BodyMd == "" {
		return nil, ErrSkillBodyEmpty
	}
	return Sanitize(ctx, sk.BodyMd)
}

func (s *service) Publish(ctx context.Context, publisherUserID uint, req PublishRequest) (mp *model.SkillMarketplace, err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-publish", publisherUserID, map[string]interface{}{
		"publisher_user_id": publisherUserID,
		"skill_id":          req.SkillID,
		"category_tags":     req.CategoryTags,
	})
	defer finalize(&err)

	// T4 dual-level publish: BOTH parent and sub-user may publish, gated by
	// ownership (can_edit predicate). Resolve caller's institution + role.
	instID, isParent, err := s.resolveCaller(ctx, publisherUserID)
	if err != nil {
		return nil, err
	}
	// Read the skill within caller's institution (parent_user_id = instID), then
	// enforce ownership. Cross-tenant / missing → ErrSkillNotOwned (no existence reveal).
	sk, err := s.getOwnedSkillForPublish(ctx, instID, req.SkillID)
	if err != nil {
		return nil, err
	}
	// Ownership: caller can publish iff X.can_edit (T4 contract G):
	//   - parent publishing own 'institution' skill (parent_user_id==callerID), OR
	//   - any caller publishing a skill they OWN (owner_user_id==callerID).
	// 'official' skills are NOT publishable via API.
	if sk.Visibility == "official" {
		return nil, ErrSkillNotOwned
	}
	canEdit := sk.OwnerUserID == publisherUserID ||
		(isParent && sk.Visibility == "institution" && sk.ParentUserID == publisherUserID)
	if !canEdit {
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
		PublisherUserID: publisherUserID, // actual publisher (parent or sub-user) — T4 contract G
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

func (s *service) List(ctx context.Context, callerUserID uint, query BrowseQuery) ([]*model.SkillMarketplace, int64, error) {
	// spec §10.1 rule 2: every biz method must verify parent. Read endpoints
	// included (§14 AC-6 requires child → 403 on /v1/marketplace/list).
	if err := s.verifyParent(ctx, callerUserID); err != nil {
		return nil, 0, err
	}
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
	// spec §10.1 rule 2: every biz method must verify parent (read endpoints included).
	if err := s.verifyParent(ctx, callerUserID); err != nil {
		return nil, err
	}
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

// Subscribe is the T4 reference-mode subscribe path (no cross-tenant clone).
//
// REPLACES the legacy two-phase clone. The subscriber gets a lightweight local
// "reference pointer" skill row in THEIR OWN tenant whose body is a snapshot of
// the marketplace SanitizedBodyMD; at runtime loadDBSkill re-reads the CURRENT
// marketplace snapshot (publisher re-publish → subscribers update). NO row is
// ever written into the publisher's tenant; the subscriber NEVER reads the
// publisher's private skill table.
//
// Single tx (no two-phase compensation needed — no external clone):
//  1. insert skill_subscription{source_skill_id, subscribed_version, cloned_skill_id=0}
//  2. insert reference-pointer skill{subscription_id, marketplace_id, visibility=institution}
//  3. subscribe_count += 1
func (s *service) Subscribe(ctx context.Context, subscriberUserID, marketplaceID uint) (subscriptionID, sourceSkillID uint, err error) {
	ctx, finalize := startMarketplaceTrace(ctx, "skill-marketplace-subscribe", subscriberUserID, map[string]interface{}{
		"subscriber_user_id": subscriberUserID,
		"marketplace_id":     marketplaceID,
	})
	defer finalize(&err)

	if err = s.verifyParent(ctx, subscriberUserID); err != nil {
		return 0, 0, err
	}
	mp, err := s.store.GetByID(ctx, marketplaceID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, 0, ErrMarketplaceNotFound
		}
		return 0, 0, fmt.Errorf("Subscribe: get marketplace: %w", err)
	}
	if !mp.IsPublic {
		return 0, 0, ErrMarketplaceNotFound
	}
	if mp.PublisherUserID == subscriberUserID {
		return 0, 0, ErrSelfSubscribeForbidden
	}

	// UNIQUE pre-check (race-tolerant; final UNIQUE constraint at DB is the
	// authoritative guard).
	if _, subErr := s.store.GetSubscription(ctx, subscriberUserID, marketplaceID); subErr == nil {
		return 0, 0, ErrAlreadySubscribed
	} else if !errors.Is(subErr, gorm.ErrRecordNotFound) {
		return 0, 0, fmt.Errorf("Subscribe: pre-check existing: %w", subErr)
	}

	// subscribed_version: best-effort read of the source skill's current Version.
	// Read is by source_skill_id within the marketplace row context — we read the
	// PUBLISHER's skill version ONLY for the hint field (not its body). If unavailable
	// (cross-tenant Get would not be reachable here), fall back to 0 = unknown.
	subscribedVersion := s.readSourceSkillVersion(ctx, mp)

	sub := &model.SkillSubscription{
		SubscriberUserID:  subscriberUserID,
		MarketplaceID:     marketplaceID,
		SourceSkillID:     mp.SourceSkillID,
		SubscribedVersion: subscribedVersion,
		ClonedSkillID:     0, // reference-mode: no clone
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		// 1. subscription row (gives us sub.ID for the pointer's subscription_id).
		if e := s.store.CreateSubscription(ctx, tx, sub); e != nil {
			return e
		}
		// 2. reference-pointer skill row in subscriber's tenant.
		if e := s.createReferencePointerTx(ctx, tx, mp, subscriberUserID, sub.ID); e != nil {
			return e
		}
		// 3. subscribe_count++.
		return s.store.IncrementSubscribeCount(ctx, tx, marketplaceID, +1)
	})
	if err != nil {
		return 0, 0, fmt.Errorf("Subscribe: tx: %w", err)
	}
	return sub.ID, mp.SourceSkillID, nil
}

// readSourceSkillVersion returns the publisher's source skill Version for the
// "原版已更新" hint. Reads directly by id (read-only, version field only — never
// surfaces the publisher's body to the subscriber). Returns 0 on any miss/error.
func (s *service) readSourceSkillVersion(ctx context.Context, mp *model.SkillMarketplace) uint {
	if mp.SourceSkillID == 0 {
		return 0
	}
	var row struct {
		Version uint `gorm:"column:version"`
	}
	err := s.db.WithContext(ctx).
		Model(&model.Skill{}).
		Select("version").
		Where("id = ?", mp.SourceSkillID).
		Scan(&row).Error
	if err != nil {
		return 0
	}
	return row.Version
}

// createReferencePointerTx inserts ONE lightweight reference-pointer skill row in
// the subscriber's tenant (T4 contract F). The row's body is seeded with the
// marketplace snapshot (renderable/non-empty) but is NON-authoritative — runtime
// loadDBSkill re-reads the marketplace's CURRENT SanitizedBodyMD by marketplace_id.
//
// Uses Select("*") to bypass the GORM default:1 is_active gotcha (database.md §6).
func (s *service) createReferencePointerTx(ctx context.Context, tx *gorm.DB, mp *model.SkillMarketplace, subscriberUserID, subscriptionID uint) error {
	enrichedDesc := fmt.Sprintf("%s\n\n[订阅自市场 / marketplace_id=%d / 订阅时间 %s]",
		mp.Description, mp.ID, time.Now().Format("2006-01-02"))

	ptr := &model.Skill{
		ParentUserID:   subscriberUserID,
		OwnerUserID:    subscriberUserID,
		Visibility:     "institution",
		Name:           mp.Name,
		Description:    enrichedDesc,
		WhenToUse:      mp.WhenToUse,
		AllowedTools:   mp.AllowedTools,
		BodyMd:         mp.SanitizedBodyMD, // snapshot seed; runtime re-reads marketplace current
		SourceType:     "imported_from_marketplace",
		OriginType:     "tenant",
		Version:        1,
		IsActive:       true,
		SubscriptionID: subscriptionID,
		MarketplaceID:  mp.ID,
		CreatedBy:      subscriberUserID,
	}
	if err := tx.WithContext(ctx).Select("*").Create(ptr).Error; err != nil {
		return fmt.Errorf("createReferencePointerTx: %w", err)
	}
	return nil
}

// Unsubscribe deletes the subscription + soft-deletes the local skill row.
//
// T4 branches on subscription mode:
//   - REFERENCE mode (source_skill_id>0, cloned_skill_id==0): the local row is a
//     reference pointer found by subscription_id. Soft-delete it + delete sub +
//     count--. No cross-tenant work (the reference holds no foreign skill).
//   - LEGACY clone mode (cloned_skill_id>0): keep the OLD cleanup path
//     (unsubscribeCleanup soft-deletes the cloned skill by id).
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

	if sub.ClonedSkillID > 0 {
		// LEGACY clone-mode path (unchanged).
		if err = s.unsubscribeCleanup(ctx, subscriberUserID, sub.ClonedSkillID, marketplaceID); err != nil {
			return err
		}
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

	// REFERENCE mode: soft-delete the pointer row (by subscription_id) + delete sub + count--.
	return s.db.Transaction(func(tx *gorm.DB) error {
		// soft-delete the reference pointer skill (subscriber's tenant only).
		if e := tx.WithContext(ctx).Model(&model.Skill{}).
			Where("subscription_id = ? AND parent_user_id = ?", sub.ID, subscriberUserID).
			Updates(map[string]interface{}{"is_active": false}).Error; e != nil {
			return fmt.Errorf("Unsubscribe: soft-delete reference pointer: %w", e)
		}
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

	// Resolve the LOCAL skill id whose bindings we count for each subscription:
	//   - REFERENCE mode (source_skill_id>0): the reference pointer skill id,
	//     looked up by subscription_id in the subscriber's tenant.
	//   - LEGACY clone mode (cloned_skill_id>0): the cloned skill id directly.
	subIDs := make([]uint, 0, len(rows))
	clonedIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		if r.Subscription.ClonedSkillID > 0 {
			clonedIDs = append(clonedIDs, r.Subscription.ClonedSkillID)
		} else {
			subIDs = append(subIDs, r.Subscription.ID)
		}
	}
	// subscription_id → pointer skill id (active rows in subscriber's tenant).
	pointerBySub := s.pointerSkillIDsBySubscription(ctx, subscriberUserID, subIDs)

	// gather all local skill ids to count bindings.
	allSkillIDs := make([]uint, 0, len(rows))
	allSkillIDs = append(allSkillIDs, clonedIDs...)
	for _, pid := range pointerBySub {
		allSkillIDs = append(allSkillIDs, pid)
	}
	counts := s.countActiveBindingsBySkillIDs(ctx, allSkillIDs)

	out := make([]SubscriptionItem, 0, len(rows))
	for _, r := range rows {
		var localSkillID uint
		if r.Subscription.ClonedSkillID > 0 {
			localSkillID = r.Subscription.ClonedSkillID
		} else {
			localSkillID = pointerBySub[r.Subscription.ID]
		}
		out = append(out, SubscriptionItem{
			Subscription: r.Subscription,
			Marketplace:  r.Marketplace,
			AgentCount:   counts[localSkillID],
		})
	}
	return out, total, nil
}

// pointerSkillIDsBySubscription maps subscription_id → active reference-pointer
// skill id in the subscriber's tenant. Scoped to parent_user_id=subscriberUserID
// so it never reads another tenant's skill rows.
func (s *service) pointerSkillIDsBySubscription(ctx context.Context, subscriberUserID uint, subIDs []uint) map[uint]uint {
	out := map[uint]uint{}
	if len(subIDs) == 0 {
		return out
	}
	type row struct {
		ID             uint `gorm:"column:id"`
		SubscriptionID uint `gorm:"column:subscription_id"`
	}
	var rows []row
	err := s.db.WithContext(ctx).
		Model(&model.Skill{}).
		Select("id, subscription_id").
		Where("subscription_id IN ? AND parent_user_id = ? AND is_active = ?", subIDs, subscriberUserID, true).
		Scan(&rows).Error
	if err != nil {
		log.Warnw("marketplace: pointerSkillIDsBySubscription failed",
			"err", err.Error(), "subscriber_user_id", subscriberUserID)
		return out
	}
	for _, r := range rows {
		out[r.SubscriptionID] = r.ID
	}
	return out
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
