# Spec · agent-mode-v2-skill-marketplace

**Status**: S2 Design
**Date**: 2026-05-24
**Author**: AI (NDF Standard track autopilot)
**Inputs**:
- [S0 Requirement card](../../../numind-server/requirements/agent-mode-v2-skill-marketplace.md)
- [S1 Proposal+PRD](../../../numind-server/proposals/agent-mode-v2-skill-marketplace-proposal.md)
- [Architecture v1 §4.3.10 跨机构脱敏共享 v2 预留接口](../../agent-mode/architecture-v1.md)

**Hard dependencies (must land develop before S4)**:
- v2 #1 `agent-mode-v2-skill-as-artifact` — provides `skill` / `skill_history` / `agent_skill_binding` tables + `biz/skill.Service` package

---

## §1 Goals & Non-Goals

### Goals

1. Cross-tenant Skill flow: publish-with-sanitization / browse / subscribe-as-copy / unsubscribe / admin-recommend
2. Sanitization pipeline integrity: regex blacklist (PII + tenant competitor list) + LLM entity recognition (qwen-turbo via `aiservice`) + frontend human-review gate (mandatory diff confirmation)
3. Subscription = copy semantics (independent snapshot, publisher edits do not affect subscriber copies)
4. Parent-account-only access; child accounts return 403
5. Langfuse trace coverage: every publish triggers one trace + one sanitize generation + ancillary spans
6. Zero regression to v2 #1 paths (`/v1/skills/*`, `/v1/agents/:id/skills/*`)

### Non-Goals

- Runtime Skill invocation (v2 #2 scope)
- Payment / revenue split
- Prod deployment
- C-side learner marketplace access
- Skill scripts/ cross-tenant share (v2.5)
- Publisher→subscriber automatic version push
- Pre-publish Skill sandbox validation (humans verify via diff)
- Admin-web batch management UI (admin endpoint only, no UI this feature)
- Platform-preset Skills via marketplace (v1 `SkillTemplate` path preserved)

---

## §2 Data Model

### §2.1 New Tables

#### `skill_marketplace`

```sql
CREATE TABLE skill_marketplace (
    id                       BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    publisher_user_id        INT UNSIGNED NOT NULL COMMENT '发布方父账户 user.id',
    source_skill_id          BIGINT UNSIGNED NOT NULL COMMENT '发布方原 skill.id 用于追溯，不级联',
    name                     VARCHAR(100) NOT NULL,
    description              VARCHAR(500) NOT NULL,
    when_to_use              VARCHAR(500) NOT NULL DEFAULT '',
    sanitized_body_md        MEDIUMTEXT NOT NULL COMMENT '脱敏后的 markdown body，独立副本',
    allowed_tools            JSON COMMENT '从原 skill 复制的 allowed_tools 列表（脱敏不动工具白名单）',
    category_tags            JSON NOT NULL COMMENT '[]string 分类标签，发布方多选',
    is_public                TINYINT(1) NOT NULL DEFAULT 1 COMMENT '1=上架, 0=下架',
    is_platform_recommended  TINYINT(1) NOT NULL DEFAULT 0 COMMENT 'admin 端打标',
    subscribe_count          INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '订阅 +1 / 取消 -1',
    created_at               DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at               DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),

    INDEX idx_publisher (publisher_user_id, is_public),
    INDEX idx_source (source_skill_id),
    INDEX idx_recommended (is_platform_recommended DESC, subscribe_count DESC, created_at DESC),
    FULLTEXT INDEX ft_search (name, description, when_to_use) /*!50700 WITH PARSER ngram */,

    CONSTRAINT fk_marketplace_publisher FOREIGN KEY (publisher_user_id) REFERENCES user(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='跨租户 Skill 发布市场';
```

**Uniqueness**: deferred to biz layer (see §3.2 D-UNIQ): "one active (is_public=1) marketplace row per source_skill_id". Enforced as `WHERE source_skill_id=? AND is_public=1` count check in transaction. Reason for not using DB partial unique index: MySQL 8 lacks partial indexes; using regular UNIQUE blocks re-publish-after-unpublish workflow.

#### `skill_subscription`

```sql
CREATE TABLE skill_subscription (
    id                  BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
    subscriber_user_id  INT UNSIGNED NOT NULL COMMENT '订阅方父账户 user.id',
    marketplace_id      BIGINT UNSIGNED NOT NULL,
    cloned_skill_id     BIGINT UNSIGNED NOT NULL COMMENT '订阅时调 biz/skill.Service.Create 新建的 skill.id',
    subscribed_at       DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    UNIQUE KEY uniq_subscriber_marketplace (subscriber_user_id, marketplace_id),
    INDEX idx_subscriber (subscriber_user_id, subscribed_at DESC),
    INDEX idx_marketplace (marketplace_id),

    CONSTRAINT fk_subscription_subscriber FOREIGN KEY (subscriber_user_id) REFERENCES user(id) ON DELETE CASCADE,
    CONSTRAINT fk_subscription_marketplace FOREIGN KEY (marketplace_id) REFERENCES skill_marketplace(id) ON DELETE CASCADE,
    CONSTRAINT fk_subscription_cloned_skill FOREIGN KEY (cloned_skill_id) REFERENCES skill(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Skill 订阅关系';
```

**Triple FK ON DELETE CASCADE**: covers three cascade cases:
- Subscriber's account deleted → subscription rows auto-removed
- Marketplace row hard-deleted (rare; admin tool) → subscriptions auto-removed
- Cloned skill hard-deleted by subscriber → subscription auto-removed

Note: v2 #1 `skill.Delete` is soft (is_active=0), so the third CASCADE rarely triggers; subscriber's cloned skill stays in DB even after unsubscribe (see §3.4 D-UNSUB).

### §2.2 Existing Table Touches

`skill` table (v2 #1): add `'subscribed'` to `source_type` enum or CHECK constraint. **Coordination with v2 #1**: see §11 D-COORD — handled via separate forward-only migration that runs **after** #1 land and **before** our migrations apply.

### §2.3 GORM Models

```go
// internal/pkg/model/skill_marketplace.go
package model

import (
    "time"
    "gorm.io/datatypes"
)

type SkillMarketplace struct {
    ID                     uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
    PublisherUserID        uint           `gorm:"not null;index:idx_publisher,priority:1" json:"publisher_user_id"`
    SourceSkillID          uint64         `gorm:"not null;index:idx_source" json:"source_skill_id"`
    Name                   string         `gorm:"size:100;not null" json:"name"`
    Description            string         `gorm:"size:500;not null" json:"description"`
    WhenToUse              string         `gorm:"size:500;not null;default:''" json:"when_to_use"`
    SanitizedBodyMD        string         `gorm:"type:mediumtext;not null" json:"sanitized_body_md"`
    AllowedTools           datatypes.JSON `gorm:"type:json" json:"allowed_tools"`
    CategoryTags           datatypes.JSON `gorm:"type:json;not null" json:"category_tags"`
    IsPublic               bool           `gorm:"not null;default:true;index:idx_publisher,priority:2" json:"is_public"`
    IsPlatformRecommended  bool           `gorm:"not null;default:false;index:idx_recommended,priority:1" json:"is_platform_recommended"`
    SubscribeCount         uint           `gorm:"not null;default:0;index:idx_recommended,priority:2" json:"subscribe_count"`
    CreatedAt              time.Time      `gorm:"not null;default:CURRENT_TIMESTAMP(3);index:idx_recommended,priority:3" json:"created_at"`
    UpdatedAt              time.Time      `gorm:"not null;default:CURRENT_TIMESTAMP(3)" json:"updated_at"`
}

func (SkillMarketplace) TableName() string { return "skill_marketplace" }
```

**default:true gotcha** (per [database.md §6](../../../.claude/rules/database.md)): `IsPublic` has `default:true`. Create path safe pattern:

```go
wantPublic := obj.IsPublic
if err := db.WithContext(ctx).Create(&obj).Error; err != nil { return err }
if !wantPublic && obj.IsPublic {
    if err := db.Model(&obj).UpdateColumn("is_public", false).Error; err != nil { return err }
    obj.IsPublic = false
}
```

Since publishing always wants `is_public=true`, this is technically defensive — but reviewer might flag, so apply consistently per existing codebase convention.

```go
// internal/pkg/model/skill_subscription.go
package model

import "time"

type SkillSubscription struct {
    ID                uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    SubscriberUserID  uint      `gorm:"not null;uniqueIndex:uniq_subscriber_marketplace,priority:1;index:idx_subscriber,priority:1" json:"subscriber_user_id"`
    MarketplaceID     uint64    `gorm:"not null;uniqueIndex:uniq_subscriber_marketplace,priority:2;index:idx_marketplace" json:"marketplace_id"`
    ClonedSkillID     uint64    `gorm:"not null" json:"cloned_skill_id"`
    SubscribedAt      time.Time `gorm:"not null;default:CURRENT_TIMESTAMP(3);index:idx_subscriber,priority:2,sort:desc" json:"subscribed_at"`
}

func (SkillSubscription) TableName() string { return "skill_subscription" }
```

### §2.4 AutoMigrate Registration

`internal/numind/helper.go` — register both models in the AutoMigrate block. **Coordination with v2 #1**: #1's helper.go block adds `Skill` / `SkillHistory` / `AgentSkillBinding`; our block adds `SkillMarketplace` / `SkillSubscription`. See §11 D-COORD for rebase strategy.

### §2.5 Migrations

Three files, all idempotent and reversible:

| File | Direction | Action |
|---|---|---|
| `20260524_120000_create_skill_marketplace.sql` | forward | CREATE TABLE skill_marketplace (full DDL above) |
| `20260524_120000_create_skill_marketplace.rollback.sql` | rollback | DROP TABLE IF EXISTS skill_marketplace |
| `20260524_120100_create_skill_subscription.sql` | forward | CREATE TABLE skill_subscription |
| `20260524_120100_create_skill_subscription.rollback.sql` | rollback | DROP TABLE IF EXISTS skill_subscription |
| `20260524_120200_skill_add_subscribed_source_type.sql` | forward | ALTER TABLE skill MODIFY COLUMN source_type ENUM('generated','custom','subscribed') NOT NULL; — depends on #1's source_type column type |
| `20260524_120200_skill_add_subscribed_source_type.rollback.sql` | rollback | ALTER TABLE skill MODIFY COLUMN source_type ENUM('generated','custom') NOT NULL; |

**Migration safety**: per CLAUDE.md memory `project_dev_deploy_migration_gap`, CI does not run migrations. S6 ndf-done after deploy, AI manually SSH dev to apply: `ssh -i $BUILD_SSH_KEY $BUILD_SSH_USER@$DEV_SSH_HOST 'mysql ... < migration.sql'`.

---

## §3 biz/marketplace Package

Five files in `numind-server/internal/numind/biz/marketplace/`:

### §3.1 `service.go` — public orchestration

```go
package marketplace

import (
    "context"
    "fmt"
    "numind-server/internal/numind/biz/skill"  // v2 #1 dependency
    "numind-server/internal/numind/store"
    "numind-server/internal/pkg/aiservice"
    "numind-server/internal/pkg/errno"
    "numind-server/internal/pkg/langfuse"
    "numind-server/internal/pkg/model"
)

type Service interface {
    SanitizePreview(ctx context.Context, publisherUserID uint, skillID uint64) (sanitizedBodyMD string, err error)
    Publish(ctx context.Context, publisherUserID uint, req PublishRequest) (*model.SkillMarketplace, error)
    Unpublish(ctx context.Context, publisherUserID uint, marketplaceID uint64) error
    List(ctx context.Context, query BrowseQuery) (items []*model.SkillMarketplace, total int64, err error)
    Get(ctx context.Context, marketplaceID uint64) (*model.SkillMarketplace, error)
    Subscribe(ctx context.Context, subscriberUserID uint, marketplaceID uint64) (clonedSkillID uint64, err error)
    Unsubscribe(ctx context.Context, subscriberUserID uint, marketplaceID uint64) error
    ListMySubscriptions(ctx context.Context, subscriberUserID uint, offset, limit int) (items []SubscriptionWithMarketplace, total int64, err error)
    SetRecommended(ctx context.Context, marketplaceID uint64, recommended bool) error
}

type PublishRequest struct {
    SkillID                  uint64   `json:"skill_id" binding:"required"`
    CategoryTags             []string `json:"category_tags" binding:"required,min=1,max=5"`
    ConfirmedSanitizedBodyMD string   `json:"confirmed_sanitized_body" binding:"required"` // frontend echoes back what user reviewed
}

type BrowseQuery struct {
    Q         string   `form:"q"`         // FULLTEXT search keyword
    Category  string   `form:"category"`  // single category filter
    Sort      string   `form:"sort"`      // "recommended" | "recent" | "popular"
    Page      int      `form:"page"`      // 1-based
    PageSize  int      `form:"page_size"` // default 20, max 100
}

type SubscriptionWithMarketplace struct {
    Subscription model.SkillSubscription `json:"subscription"`
    Marketplace  model.SkillMarketplace  `json:"marketplace"`
    AgentCount   int                     `json:"agent_count"` // how many of subscriber's agents have this cloned skill bound
}

type service struct {
    store        store.IMarketplaceStore
    skillSvc     skill.Service     // v2 #1 dependency
    skillStore   store.ISkillStore // v2 #1 dependency
    userStore    store.IUserStore
}

func NewService(
    s store.IMarketplaceStore,
    skillSvc skill.Service,
    skillStore store.ISkillStore,
    userStore store.IUserStore,
) Service {
    return &service{store: s, skillSvc: skillSvc, skillStore: skillStore, userStore: userStore}
}
```

#### Method behaviors

**`SanitizePreview`** (dry-run, no DB write):
1. Load skill by ID; verify owner is publisherUserID and is parent account
2. Call `sanitize.Run(ctx, body, frontmatter, publisherUserID)` (see §3.2)
3. Return sanitized body to frontend for diff display
4. **Langfuse**: starts trace "skill-marketplace-sanitize-preview", child generation for LLM call

**`Publish`**:
1. Verify parent account (`user.parent_user_id IS NULL`) → else `errno.ErrChildAccountCannotAccessMarketplace`
2. Load skill, verify owned by publisher, not deleted (is_active=1)
3. Check uniqueness (§3.2 D-UNIQ): no existing active marketplace row for same source_skill_id
4. **Re-run sanitize** (don't trust frontend `confirmed_sanitized_body` blindly — compare hash with current sanitize output; tolerate small whitespace diff but reject substantial differences indicating tampering)
5. In transaction: insert SkillMarketplace row
6. **Langfuse**: trace "skill-marketplace-publish", attached metadata `{user_id, skill_id, marketplace_id}`

**`Unpublish`**:
1. Verify ownership
2. UPDATE `is_public=0` (soft unpublish; existing subscriptions remain valid, but new browses won't see)

**`List`**:
1. Build query: optional FULLTEXT match against (q), optional JSON_CONTAINS(category_tags, '"<cat>"')
2. WHERE `is_public=1`
3. ORDER BY by sort param:
   - `recommended` (default): `is_platform_recommended DESC, subscribe_count DESC, created_at DESC`
   - `recent`: `created_at DESC`
   - `popular`: `subscribe_count DESC, created_at DESC`
4. LIMIT/OFFSET pagination

**`Get`**:
1. Load marketplace row by ID
2. Verify `is_public=1` OR caller is publisher (so publisher can preview their unpublished items)

**`Subscribe`** (the critical path):
1. Verify subscriber is parent account
2. Load marketplace row, verify `is_public=1`
3. Verify `publisher_user_id != subscriberUserID` (no self-subscribe)
4. Check UNIQUE violation pre-check (subscription already exists) → `errno.ErrAlreadySubscribed`
5. **In transaction (the atomicity gate)**:
   - Call `s.skillSvc.Create(ctx, subscriberUserID, skill.CreateRequest{Name: mp.Name, Description: mp.Description, WhenToUse: mp.WhenToUse, AllowedTools: mp.AllowedTools, BodyMD: mp.SanitizedBodyMD, SourceType: "subscribed"})` — yields `clonedSkill.ID`
   - Insert SkillSubscription `{SubscriberUserID, MarketplaceID, ClonedSkillID}`
   - UPDATE marketplace SET `subscribe_count = subscribe_count + 1`
6. Return clonedSkillID
7. **Langfuse**: trace "skill-marketplace-subscribe", span "clone-to-subscriber"

**`Unsubscribe`**:
1. Find subscription by `(subscriberUserID, marketplaceID)` → 404 if missing
2. **In transaction**:
   - Soft-delete cloned skill via `s.skillSvc.Delete(ctx, sub.ClonedSkillID)` (is_active=0, bindings cascade per #1)
   - Delete SkillSubscription row (hard delete)
   - UPDATE marketplace SET `subscribe_count = GREATEST(subscribe_count - 1, 0)` (defensive against double-decrement)
3. Return success

**`ListMySubscriptions`**: JOIN skill_subscription + skill_marketplace, LEFT JOIN agent_skill_binding count agent count, ORDER BY subscribed_at DESC, pagination.

**`SetRecommended`** (admin-only — caller path through admin_controller already enforces admin token):
1. Verify marketplace exists
2. UPDATE `is_platform_recommended = ?`

### §3.2 `sanitize.go` — sanitization pipeline

```go
package marketplace

import (
    "context"
    "encoding/json"
    "fmt"
    "regexp"
    "strings"
    "numind-server/internal/pkg/aiservice"
    "numind-server/internal/pkg/langfuse"
)

type SanitizeResult struct {
    SanitizedBodyMD string
    Stages          []string // ["regex", "llm"]
    LLMTokens       struct{ Prompt, Completion int }
}

var (
    rePII = []struct {
        Name    string
        Pattern *regexp.Regexp
        Replace string
    }{
        {"email", regexp.MustCompile(`[\w._%+-]+@[\w.-]+\.[A-Za-z]{2,}`), "[邮箱]"},
        {"phone_cn", regexp.MustCompile(`1[3-9]\d{9}`), "[手机]"},
        {"id_card_cn", regexp.MustCompile(`[1-9]\d{16}[0-9Xx]`), "[身份证]"},
        {"bank_card", regexp.MustCompile(`\b\d{16,19}\b`), "[银行卡]"},
    }
)

const sanitizePromptKey = "skill-marketplace-sanitize-v1"

const sanitizeFallbackPrompt = `你是脱敏助手。请识别以下 markdown 文本中的：
- 具体人名（学员、员工）→ 替换为 [姓名]
- 具体机构名（公司、学校）→ 替换为 [机构]
- 具体产品名/课程名 → 替换为 [产品]
保留行业通用术语和职能描述。仅返回脱敏后的完整 markdown，不要添加任何额外说明。

---原文---
%s
---原文结束---`

func (s *service) sanitize(ctx context.Context, body string, publisherUserID uint) (SanitizeResult, error) {
    // Stage 1: regex blacklist (PII + tenant competitor names)
    stage1 := applyRegexBlacklist(ctx, body, publisherUserID, s.userStore)

    // Stage 2: LLM entity recognition
    stage2, tokens, err := s.callSanitizeLLM(ctx, stage1)
    if err != nil {
        return SanitizeResult{}, fmt.Errorf("sanitize: %w", err)
    }

    return SanitizeResult{
        SanitizedBodyMD: stage2,
        Stages:          []string{"regex", "llm"},
        LLMTokens:       tokens,
    }, nil
}

func applyRegexBlacklist(ctx context.Context, body string, publisherUserID uint, userStore store.IUserStore) string {
    // PII patterns
    out := body
    for _, p := range rePII {
        out = p.Pattern.ReplaceAllString(out, p.Replace)
    }
    // Tenant competitor list (from agent_permission_config.forbidden_competitor_names)
    competitors, err := userStore.GetForbiddenCompetitors(ctx, publisherUserID)
    if err == nil {
        for _, name := range competitors {
            if name == "" { continue }
            out = strings.ReplaceAll(out, name, "[竞品]")
        }
    }
    return out
}

func (s *service) callSanitizeLLM(ctx context.Context, body string) (sanitized string, tokens struct{ Prompt, Completion int }, err error) {
    // Wrap in Langfuse span if trace context exists
    tc := langfuse.FromContext(ctx)
    var genID string
    if tc != nil {
        genID = langfuse.SpanID()
        langfuse.CreateGeneration(tc.TraceID, genID,
            langfuse.WithGenParent(tc.ParentObservationID),
            langfuse.WithGenName("sanitize-skill-body"),
            langfuse.WithGenModel("qwen-turbo"),
            langfuse.WithGenInput(map[string]string{"body": body}),
        )
        defer langfuse.EndGeneration(genID)
    }

    // Fetch prompt from Langfuse, fallback to inline
    promptTpl, err := langfuse.FetchPrompt(ctx, sanitizePromptKey)
    if err != nil || promptTpl == "" {
        promptTpl = sanitizeFallbackPrompt
    }
    prompt := fmt.Sprintf(promptTpl, body)

    resp, err := aiservice.Chat(ctx, aiservice.ChatRequest{
        TaskID: "skill.marketplace.sanitize",
        Messages: []aiservice.Message{
            {Role: "user", Content: prompt},
        },
        Temperature: 0.1,
        MaxTokens:   8000,
    })
    if err != nil {
        if tc != nil && genID != "" {
            langfuse.UpdateGeneration(genID, langfuse.WithGenOutput(map[string]string{"error": err.Error()}))
        }
        return "", tokens, fmt.Errorf("aiservice.Chat: %w", err)
    }

    sanitized = strings.TrimSpace(resp.Content)
    tokens.Prompt = resp.PromptTokens
    tokens.Completion = resp.CompletionTokens

    if tc != nil && genID != "" {
        langfuse.UpdateGeneration(genID,
            langfuse.WithGenOutput(sanitized),
            langfuse.WithGenUsage(tokens.Prompt, tokens.Completion),
        )
    }
    return sanitized, tokens, nil
}
```

**Key design decisions**:

- **D-UNIQ**: Uniqueness on `(source_skill_id, is_public=1)` enforced in biz layer via SELECT inside transaction. Rationale: MySQL 8 lacks partial unique index; full UNIQUE blocks re-publish-after-unpublish. The race window between SELECT and INSERT is acceptable for low-frequency publish; if double-publish slips through, admin can manually unpublish the dup.
- **D-TENANT-COMP**: tenant competitor list fetched from `agent_permission_config.forbidden_competitor_names` JSON field (v1 #5 existing). Falls back to empty list if user has no permission config row.
- **D-PROMPT**: prompt template stored in Langfuse with key `skill-marketplace-sanitize-v1`; falls back to inline if Langfuse fetch fails (matches existing `agent.dialectic` prompt pattern from v1.5 memory layer).
- **D-LLM-FAIL**: LLM call failure → return error, publish path returns `errno.ErrSanitizeUnavailable` → frontend disables publish button. **No bypass**.
- **D-CONFIRM**: Publish endpoint receives `confirmed_sanitized_body` from frontend but does NOT trust it blindly. Re-runs sanitize internally and compares (lossy hash: normalize whitespace, then SHA-256). Mismatch within 5% character delta tolerated (LLM non-determinism); larger mismatch rejected as tampering → `errno.ErrSanitizeConfirmationMismatch`.

### §3.3 `clone.go` — subscription copy logic

```go
package marketplace

import (
    "context"
    "fmt"
    "gorm.io/gorm"
    "numind-server/internal/numind/biz/skill"
    "numind-server/internal/pkg/langfuse"
    "numind-server/internal/pkg/model"
)

func (s *service) cloneToSubscriber(ctx context.Context, tx *gorm.DB, mp *model.SkillMarketplace, subscriberUserID uint) (uint64, error) {
    tc := langfuse.FromContext(ctx)
    var spanID string
    if tc != nil {
        spanID = langfuse.SpanID()
        langfuse.CreateSpan(tc.TraceID, spanID,
            langfuse.WithSpanParent(tc.ParentObservationID),
            langfuse.WithSpanName("marketplace-subscribe-clone"),
            langfuse.WithSpanInput(map[string]interface{}{
                "subscriber_user_id": subscriberUserID,
                "marketplace_id":     mp.ID,
            }),
        )
        defer langfuse.EndSpan(spanID)
    }

    // Inject source provenance into description (so subscriber sees where it came from)
    enrichedDesc := fmt.Sprintf("%s\n\n[订阅自市场 / marketplace_id=%d / 订阅时间 %s]",
        mp.Description, mp.ID, time.Now().Format("2006-01-02"))

    createReq := skill.CreateRequest{
        Name:         mp.Name,
        Description:  enrichedDesc,
        WhenToUse:    mp.WhenToUse,
        AllowedTools: mp.AllowedTools,
        BodyMD:       mp.SanitizedBodyMD,
        SourceType:   "subscribed",
    }
    // Use the transaction-bound store path on skill.Service if available; else fall back to non-tx and accept potential orphan window (mitigated by §6 cleanup cron)
    clonedID, err := s.skillSvc.CreateInTx(ctx, tx, subscriberUserID, createReq)
    if err != nil {
        return 0, fmt.Errorf("skill.Service.CreateInTx: %w", err)
    }
    return clonedID, nil
}
```

**Dependency on v2 #1**: requires `skill.Service.CreateInTx(ctx, tx, userID, req)` method that participates in caller's transaction. If #1 only exposes `Create(ctx, userID, req)` (no tx parameter), we need a coordination patch (see §11 D-COORD).

**Fallback if #1 has no Tx variant**: split into two-phase commit:
1. Create cloned skill outside tx (yields clonedID)
2. Start tx: insert subscription row + update subscribe_count
3. On tx rollback: defer-call `skill.Delete(clonedID)` to clean up orphan

Two-phase is acceptable but riskier — prefer pushing `CreateInTx` into #1 via PR.

### §3.4 `search.go` — browse query builder

```go
package marketplace

import (
    "context"
    "fmt"
    "strings"
    "gorm.io/gorm"
    "numind-server/internal/pkg/model"
)

type listOptions struct {
    Q        string
    Category string
    Sort     string
    Offset   int
    Limit    int
}

func (s *service) buildListQuery(db *gorm.DB, opts listOptions) *gorm.DB {
    q := db.Model(&model.SkillMarketplace{}).Where("is_public = ?", true)

    if opts.Q != "" {
        // FULLTEXT match with boolean mode for partial matching
        q = q.Where("MATCH(name, description, when_to_use) AGAINST(? IN BOOLEAN MODE)", booleanModeQuery(opts.Q))
    }
    if opts.Category != "" {
        q = q.Where("JSON_CONTAINS(category_tags, JSON_QUOTE(?))", opts.Category)
    }

    switch opts.Sort {
    case "recent":
        q = q.Order("created_at DESC")
    case "popular":
        q = q.Order("subscribe_count DESC, created_at DESC")
    default: // "recommended"
        q = q.Order("is_platform_recommended DESC, subscribe_count DESC, created_at DESC")
    }
    return q
}

func booleanModeQuery(q string) string {
    // Escape FULLTEXT boolean mode special chars, wrap each token with + and *
    tokens := strings.Fields(q)
    out := make([]string, 0, len(tokens))
    for _, t := range tokens {
        clean := strings.ReplaceAll(t, "*", "")
        clean = strings.ReplaceAll(clean, "+", "")
        clean = strings.ReplaceAll(clean, "-", "")
        clean = strings.ReplaceAll(clean, "\"", "")
        if clean == "" { continue }
        out = append(out, fmt.Sprintf("+%s*", clean))
    }
    return strings.Join(out, " ")
}
```

**Key design decisions**:

- **D-FT-BOOLEAN**: ngram parser + BOOLEAN MODE for partial matching ("销售" matches "销售调研"). Tokens AND-joined to narrow results.
- **D-SORT-DEFAULT**: `recommended` is default (matches PRD UX expectation).
- **D-UNSUB**: unsubscribe only soft-deletes cloned_skill (is_active=0); hard-delete of subscription row is final. If subscriber re-subscribes later, a fresh clone is created (new skill row, new subscription row).

### §3.5 `admin.go` — admin-side methods

```go
package marketplace

import (
    "context"
    "fmt"
    "numind-server/internal/pkg/errno"
)

func (s *service) SetRecommended(ctx context.Context, marketplaceID uint64, recommended bool) error {
    mp, err := s.store.GetByID(ctx, marketplaceID)
    if err != nil {
        return errno.ErrNotFound.SetMessage(fmt.Sprintf("marketplace %d not found", marketplaceID))
    }
    return s.store.UpdateRecommended(ctx, mp.ID, recommended)
}
```

No additional admin business logic this feature — recommend is the only admin endpoint.

---

## §4 API Contracts

### §4.1 User endpoints (`/v1/marketplace/*`)

All endpoints require user_token JWT + biz-layer parent-account check.

#### POST `/v1/marketplace/sanitize-preview`

**Request**:
```json
{ "skill_id": 123 }
```
**Response (200)**:
```json
{
  "code": 0, "message": "ok",
  "data": {
    "sanitized_body_md": "脱敏后的 markdown 全文...",
    "stages_applied": ["regex", "llm"],
    "llm_tokens": { "prompt": 1234, "completion": 1100 }
  }
}
```
**Errors**: 403 child account, 404 skill not found, 403 not owner, 503 sanitize unavailable

#### POST `/v1/marketplace/publish`

**Request**:
```json
{
  "skill_id": 123,
  "category_tags": ["销售", "调研"],
  "confirmed_sanitized_body": "脱敏后的 markdown..."
}
```
**Response (200)**:
```json
{
  "code": 0, "message": "ok",
  "data": {
    "id": 456,
    "publisher_user_id": 10,
    "source_skill_id": 123,
    "name": "销售调研",
    "is_public": true,
    "subscribe_count": 0,
    "created_at": "2026-05-24T12:00:00.000+08:00"
  }
}
```
**Errors**: 403, 404, 409 already-published-active, 422 confirmation mismatch, 503

#### GET `/v1/marketplace/list?q=...&category=...&sort=...&page=1&page_size=20`

**Response (200)**:
```json
{
  "code": 0, "message": "ok",
  "data": {
    "list": [
      {
        "id": 456,
        "name": "销售调研",
        "description": "...",
        "publisher_display_name": "某机构",
        "category_tags": ["销售"],
        "is_platform_recommended": true,
        "subscribe_count": 12,
        "i_subscribed": true,
        "created_at": "..."
      }
    ],
    "total": 87,
    "page": 1,
    "page_size": 20
  }
}
```
**Note**: `i_subscribed` is computed by joining `skill_subscription` filtered by caller's user_id.

#### GET `/v1/marketplace/:id`

**Response (200)**:
```json
{
  "code": 0, "message": "ok",
  "data": {
    "id": 456,
    "publisher_user_id": 10,
    "publisher_display_name": "某机构",
    "source_skill_id": 123,
    "name": "销售调研",
    "description": "...",
    "when_to_use": "...",
    "sanitized_body_md": "完整 markdown...",
    "allowed_tools": ["web_search", "file_read"],
    "category_tags": ["销售"],
    "is_public": true,
    "is_platform_recommended": true,
    "subscribe_count": 12,
    "i_subscribed": true,
    "my_cloned_skill_id": 789,
    "created_at": "..."
  }
}
```

#### POST `/v1/marketplace/:id/subscribe`

**Response (200)**:
```json
{
  "code": 0, "message": "ok",
  "data": { "cloned_skill_id": 789, "subscription_id": 321 }
}
```
**Errors**: 403, 404 unpublished, 409 already subscribed, 409 self-subscribe

#### DELETE `/v1/marketplace/:id/unsubscribe`

**Response (200)**: `{ "code": 0, "message": "ok", "data": null }`
**Errors**: 404 no subscription

#### GET `/v1/marketplace/my-subscriptions?page=1&page_size=20`

**Response (200)**:
```json
{
  "code": 0, "message": "ok",
  "data": {
    "list": [
      {
        "subscription": { "id": 321, "subscribed_at": "..." },
        "marketplace": { "id": 456, "name": "销售调研", ... },
        "cloned_skill_id": 789,
        "agent_count": 2
      }
    ],
    "total": 5
  }
}
```

### §4.2 Admin endpoint (`/v1/admin/marketplace/*`)

#### POST `/v1/admin/marketplace/:id/recommend`

**Request**: `{ "recommended": true }`
**Response (200)**: `{ "code": 0, "message": "ok", "data": null }`
**Auth**: admin_token middleware
**Errors**: 404

### §4.3 Errno additions (`internal/pkg/errno/`)

```go
// Marketplace errors
var (
    ErrChildAccountCannotAccessMarketplace = NewErrno(401001, "子账户无法访问技能市场")
    ErrSkillNotOwned                       = NewErrno(401002, "无权操作此技能")
    ErrSkillAlreadyPublished               = NewErrno(401003, "该技能已上架，请先下架再重新发布")
    ErrSelfSubscribeForbidden              = NewErrno(401004, "不能订阅自己发布的技能")
    ErrAlreadySubscribed                   = NewErrno(401005, "已订阅该技能")
    ErrMarketplaceNotFound                 = NewErrno(401006, "市场项目不存在或已下架")
    ErrSubscriptionNotFound                = NewErrno(401007, "订阅记录不存在")
    ErrSanitizeUnavailable                 = NewErrno(401008, "脱敏服务暂不可用，请稍后重试")
    ErrSanitizeConfirmationMismatch        = NewErrno(401009, "脱敏内容与确认不符，请重新预览")
)
```

Error code range 401xxx is the marketplace block (per existing errno conventions — search for nearest unused range during S4).

---

## §5 Controllers

### §5.1 User controller (`internal/numind/controller/v1/marketplace.go`)

Standard Gin handlers — thin wrappers per CLAUDE.md §3 (controller does binding + auth extraction + biz call + response only).

```go
package v1

import (
    "github.com/gin-gonic/gin"
    "strconv"
    "numind-server/internal/numind/biz/marketplace"
    "numind-server/internal/pkg/core"
    "numind-server/internal/pkg/errno"
)

type MarketplaceController struct {
    svc marketplace.Service
}

func NewMarketplaceController(svc marketplace.Service) *MarketplaceController {
    return &MarketplaceController{svc: svc}
}

func (c *MarketplaceController) SanitizePreview(g *gin.Context) {
    var req struct { SkillID uint64 `json:"skill_id" binding:"required"` }
    if err := g.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(g, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }
    userID := g.GetUint("userID")
    sanitized, err := c.svc.SanitizePreview(g.Request.Context(), userID, req.SkillID)
    core.WriteResponse(g, err, gin.H{"sanitized_body_md": sanitized})
}

// Publish, Unpublish, List, Get, Subscribe, Unsubscribe, ListMySubscriptions — same shape
```

### §5.2 Admin controller (`internal/numind/controller/v1/admin_marketplace.go`)

```go
func (c *AdminMarketplaceController) SetRecommended(g *gin.Context) {
    id, err := strconv.ParseUint(g.Param("id"), 10, 64)
    if err != nil {
        core.WriteResponse(g, errno.ErrBind.SetMessage("invalid id"), nil)
        return
    }
    var req struct { Recommended bool `json:"recommended"` }
    if err := g.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(g, errno.ErrBind.SetMessage(err.Error()), nil)
        return
    }
    core.WriteResponse(g, c.svc.SetRecommended(g.Request.Context(), id, req.Recommended), nil)
}
```

---

## §6 Router Registration

### §6.1 `internal/numind/router.go` (user endpoints)

Add new group under existing user_token group:

```go
marketplace := userGroup.Group("/marketplace")
{
    marketplace.POST("/sanitize-preview", mpCtl.SanitizePreview)
    marketplace.POST("/publish", mpCtl.Publish)
    marketplace.POST("/:id/unpublish", mpCtl.Unpublish)
    marketplace.GET("/list", mpCtl.List)
    marketplace.GET("/my-subscriptions", mpCtl.ListMySubscriptions)  // must precede /:id route
    marketplace.GET("/:id", mpCtl.Get)
    marketplace.POST("/:id/subscribe", mpCtl.Subscribe)
    marketplace.DELETE("/:id/unsubscribe", mpCtl.Unsubscribe)
}
```

**Gin path order**: `/my-subscriptions` must be registered before `/:id` to avoid Gin matching it as `id=my-subscriptions`.

### §6.2 `internal/numind/admin_router.go` (admin endpoint)

```go
adminMarketplace := adminGroup.Group("/marketplace")
{
    adminMarketplace.POST("/:id/recommend", adminMpCtl.SetRecommended)
}
```

---

## §7 Store Layer (`internal/numind/store/marketplace.go`)

```go
package store

import (
    "context"
    "gorm.io/gorm"
    "numind-server/internal/pkg/model"
)

type IMarketplaceStore interface {
    Create(ctx context.Context, mp *model.SkillMarketplace) error
    GetByID(ctx context.Context, id uint64) (*model.SkillMarketplace, error)
    GetActiveBySourceSkillID(ctx context.Context, sourceSkillID uint64) (*model.SkillMarketplace, error)
    UpdateIsPublic(ctx context.Context, id uint64, isPublic bool) error
    UpdateRecommended(ctx context.Context, id uint64, recommended bool) error
    IncrementSubscribeCount(ctx context.Context, tx *gorm.DB, id uint64, delta int) error
    List(ctx context.Context, opts listOptions) ([]*model.SkillMarketplace, int64, error)

    CreateSubscription(ctx context.Context, tx *gorm.DB, sub *model.SkillSubscription) error
    DeleteSubscription(ctx context.Context, tx *gorm.DB, subscriberUserID uint, marketplaceID uint64) error
    GetSubscription(ctx context.Context, subscriberUserID uint, marketplaceID uint64) (*model.SkillSubscription, error)
    ListMySubscriptions(ctx context.Context, subscriberUserID uint, offset, limit int) ([]SubscriptionWithMarketplace, int64, error)
}
```

Implementation uses GORM standard patterns (per [database.md §3-4](../../../.claude/rules/database.md)). All transaction-scoped methods accept `*gorm.DB` so callers can pass either `db` or `tx` interchangeably.

---

## §8 Frontend (numind-web-v3)

### §8.1 Pinia store (`src/stores/marketplace.ts`)

```typescript
import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import * as api from '@/api/marketplace'

export const useMarketplaceStore = defineStore('marketplace', () => {
  // List page state
  const items = ref<MarketplaceItem[]>([])
  const total = ref(0)
  const loading = ref(false)
  const queryQ = ref('')
  const queryCategory = ref('')
  const querySort = ref<'recommended' | 'recent' | 'popular'>('recommended')
  const queryPage = ref(1)

  // Detail page state
  const currentItem = ref<MarketplaceItemDetail | null>(null)

  // My subscriptions
  const mySubscriptions = ref<SubscriptionWithMarketplace[]>([])

  // Publish flow state
  const sanitizePreviewLoading = ref(false)
  const sanitizePreviewError = ref<string | null>(null)
  const sanitizePreviewResult = ref<string | null>(null)

  async function fetchList() {
    loading.value = true
    try {
      const res = await api.listMarketplace({
        q: queryQ.value, category: queryCategory.value,
        sort: querySort.value, page: queryPage.value, page_size: 20
      })
      items.value = res.data.list
      total.value = res.data.total
    } finally { loading.value = false }
  }

  async function fetchDetail(id: number) { /* ... */ }
  async function subscribe(id: number) { /* ... */ }
  async function unsubscribe(id: number) { /* ... */ }
  async function fetchMySubscriptions() { /* ... */ }

  async function sanitizePreview(skillId: number) {
    sanitizePreviewLoading.value = true
    sanitizePreviewError.value = null
    try {
      const res = await api.sanitizePreview(skillId)
      sanitizePreviewResult.value = res.data.sanitized_body_md
    } catch (e: any) {
      sanitizePreviewError.value = e?.response?.data?.message || '脱敏服务暂不可用'
    } finally { sanitizePreviewLoading.value = false }
  }

  async function publish(skillId: number, categoryTags: string[], confirmedBody: string) { /* ... */ }

  return {
    items, total, loading, queryQ, queryCategory, querySort, queryPage,
    currentItem, mySubscriptions,
    sanitizePreviewLoading, sanitizePreviewError, sanitizePreviewResult,
    fetchList, fetchDetail, subscribe, unsubscribe, fetchMySubscriptions,
    sanitizePreview, publish
  }
})
```

### §8.2 API layer (`src/api/marketplace.ts`)

```typescript
import request from '@/api/request'

export interface MarketplaceItem {
  id: number
  name: string
  description: string
  publisher_display_name: string
  category_tags: string[]
  is_platform_recommended: boolean
  subscribe_count: number
  i_subscribed: boolean
  created_at: string
}

export interface MarketplaceItemDetail extends MarketplaceItem {
  publisher_user_id: number
  source_skill_id: number
  when_to_use: string
  sanitized_body_md: string
  allowed_tools: string[]
  is_public: boolean
  my_cloned_skill_id?: number
}

export const listMarketplace = (params: { q?: string; category?: string; sort?: string; page: number; page_size: number }) =>
  request.get<{ list: MarketplaceItem[]; total: number; page: number; page_size: number }>('/v1/marketplace/list', { params })

export const getMarketplace = (id: number) =>
  request.get<MarketplaceItemDetail>(`/v1/marketplace/${id}`)

export const subscribeMarketplace = (id: number) =>
  request.post<{ cloned_skill_id: number; subscription_id: number }>(`/v1/marketplace/${id}/subscribe`)

export const unsubscribeMarketplace = (id: number) =>
  request.delete(`/v1/marketplace/${id}/unsubscribe`)

export const listMySubscriptions = (params: { page: number; page_size: number }) =>
  request.get('/v1/marketplace/my-subscriptions', { params })

export const sanitizePreview = (skillId: number) =>
  request.post<{ sanitized_body_md: string; stages_applied: string[]; llm_tokens: { prompt: number; completion: number } }>('/v1/marketplace/sanitize-preview', { skill_id: skillId })

export const publishMarketplace = (req: { skill_id: number; category_tags: string[]; confirmed_sanitized_body: string }) =>
  request.post<MarketplaceItem>('/v1/marketplace/publish', req)

export const unpublishMarketplace = (id: number) =>
  request.post(`/v1/marketplace/${id}/unpublish`)
```

### §8.3 Views

4 views in `src/views/marketplace/`:

| File | Route | Purpose | Key state handling |
|---|---|---|---|
| `MarketplaceBrowse.vue` | `/marketplace` | Browse cards | loading skeleton / empty CTA / error retry / success grid |
| `MarketplaceDetail.vue` | `/marketplace/:id` | Detail markdown render + subscribe button | 4 states; subscribe modal二次确认 |
| `MarketplaceSubscribed.vue` | `/marketplace/subscribed` | DataTable of my subscriptions | 4 states; row action: 取消订阅 / 装载到 Agent |
| `MarketplacePublish.vue` | `/marketplace/publish/:skill_id` | Diff view + confirmation gate | sanitize loading / LLM error / success diff render |

**`MarketplacePublish.vue` skeleton**:

```vue
<template>
  <div class="publish-page">
    <h1>发布到市场</h1>
    <div class="meta-form">
      <CategoryMultiSelect v-model="selectedTags" />
    </div>
    <div v-if="store.sanitizePreviewLoading" class="loading">脱敏中...</div>
    <div v-else-if="store.sanitizePreviewError" class="error">
      {{ store.sanitizePreviewError }}
      <button @click="retrySanitize">重试</button>
    </div>
    <div v-else-if="store.sanitizePreviewResult" class="diff-view">
      <VueDiff :left="originalBody" :right="store.sanitizePreviewResult" />
      <label class="confirm-gate">
        <input type="checkbox" v-model="confirmed" />
        我已确认脱敏内容无敏感信息
      </label>
      <button :disabled="!confirmed || !selectedTags.length" @click="doPublish">
        发布
      </button>
    </div>
  </div>
</template>
```

Uses `vue-diff` npm package for left-right diff rendering.

### §8.4 Cross-cutting UI

- `src/views/config/skills/SkillEditor.vue` (v2 #1): add `<button @click="goPublish">发布到市场</button>` near top action bar
- `src/views/config/skills/SkillList.vue` (v2 #1): when joining marketplace data, render `<Badge>已发布</Badge>` on rows where source has active marketplace entry
- `src/layouts/AppLayout.vue` (existing): add menu item `{ to: '/marketplace', label: '技能市场', requiresParent: true }`
- `src/router/index.ts`: register 4 new routes with route guard `meta: { requiresParent: true }` (existing pattern from agent-mode-configurator-relocate)

---

## §9 Langfuse Trace Topology (Per `ai-service.md` §1-3)

| Trace | When | Generations | Spans |
|---|---|---|---|
| `skill-marketplace-sanitize-preview` | POST /sanitize-preview entry | `sanitize-skill-body` (qwen-turbo) | `sanitize-regex-stage` |
| `skill-marketplace-publish` | POST /publish entry | `sanitize-skill-body` (qwen-turbo, re-run for verification) | `sanitize-regex-stage`, `publish-uniqueness-check` |
| `skill-marketplace-subscribe` | POST /:id/subscribe entry | (none — no LLM) | `marketplace-subscribe-clone` |
| `skill-marketplace-unsubscribe` | DELETE /:id/unsubscribe entry | (none) | `marketplace-unsubscribe-cleanup` |

All traces include `user_id` metadata. Sanitize generations include `model="qwen-turbo"` + token usage from `aiservice.Chat` response.

**Why traces on no-LLM paths (subscribe/unsubscribe)**: per memory `feedback_review_each_stage` — observability is non-negotiable for cross-tenant operations. Subscribe writes to TWO tenants' data; full trace lets us audit "who subscribed what from whom and when" via Langfuse dashboard.

---

## §10 Security Analysis

### §10.1 Cross-tenant data isolation

**Hard rules**:
1. `subscriberUserID` and `publisherUserID` always taken from JWT context, never from request body/params
2. Every biz method first checks `user.parent_user_id IS NULL` → child accounts blocked
3. `Publish`: verify `skill.parent_user_id == publisherUserID` (skill owned by publisher)
4. `Subscribe`: verify `publisherUserID != subscriberUserID` (no self-subscribe)
5. `Unsubscribe`: verify subscription `subscriber_user_id == jwtUserID`
6. `Get/List`: filter `is_public=1` OR `publisher_user_id == jwtUserID` (publisher can see own unpublished)
7. `cloneToSubscriber` writes to skill table with `parent_user_id=subscriberUserID` — relies on v2 #1's `skill.Service.Create` honoring the userID parameter strictly

**Test coverage requirement**: unit test EVERY biz method with cross-tenant scenario (A's JWT trying to act on B's data → 403/404).

### §10.2 Sanitization integrity

- Two-stage pipeline: regex (deterministic, fast) + LLM (context-aware, fallible)
- Frontend diff-confirmation is the LAST line of defense — backend verifies confirmation hash matches re-run sanitize output
- Failure mode: no bypass. LLM down → publish disabled.

### §10.3 Audit trail

- Every publish/subscribe/unsubscribe creates Langfuse trace
- admin SetRecommended already audited via admin_token middleware
- Future: marketplace operations could write to `membership_event`-style audit log if needed (out of scope this feature)

### §10.4 SQL injection / FULLTEXT safety

- Search keyword `q` passed through parameterized query
- `booleanModeQuery()` strips boolean-mode special chars (+, -, *, ") before re-applying them in controlled fashion
- Category param compared via `JSON_CONTAINS(..., JSON_QUOTE(?))` parameterized

---

## §11 Coordination with v2 #1 (D-COORD)

**Problem**: this feature depends on v2 #1's `skill` table, `biz/skill.Service.Create`, and `agent_skill_binding` table — none of which exist yet on develop.

**Strategy**:

1. **S0-S3 (artifact authoring)**: no code touched, no dependency
2. **S3→S4 hard gate**: explicit check (see [task #5](../../../numind-server/scripts/ndf/) — we'll inline the check):
   ```bash
   cd numind-server && git fetch origin develop
   # #1 land detection: requires skill table migration AND biz/skill/service.go
   git log origin/develop --oneline | grep -E "skill-as-artifact|create.*skill.*table" | head -3
   git show origin/develop:internal/numind/biz/skill/service.go 2>&1 | head -3
   git show origin/develop:internal/pkg/model/skill.go 2>&1 | head -3
   ```
   - All present → proceed to S4
   - Missing → ScheduleWakeup 1800s loop, max 7 days, then Pause and Ask

3. **S4 entry**: in worktree, `git fetch origin develop && git rebase origin/develop` — pulls in #1's changes; conflicts only in shared files (router.go, helper.go) which we'll address one by one

4. **`skill.Service.CreateInTx` requirement**: if #1 only exposes `Create(ctx, userID, req)` without tx parameter, we need to add `CreateInTx(ctx, tx, userID, req)`. Options:
   - **Option A (preferred)**: post-land-#1, send a small PR to add `CreateInTx` to #1's package — clean separation
   - **Option B (fallback)**: implement two-phase commit in clone.go (create skill outside tx, then tx for subscription + cleanup defer) — works but has window for orphan rows
   - S4 task ordering: detect which #1 actually shipped, then pick A or B

5. **`skill.source_type` ENUM extension**: write a forward-only migration `20260524_120200_skill_add_subscribed_source_type.sql` to ALTER COLUMN to add `'subscribed'` to enum. Idempotent via `INFORMATION_SCHEMA.COLUMNS` check.

6. **Shared files in S4**: rebase carefully on `numind-server/internal/numind/router.go` (add /v1/marketplace group), `numind-server/internal/numind/helper.go` (AutoMigrate registration), `numind-web-v3/src/router/index.ts` (add /marketplace routes), `numind-web-v3/src/views/config/skills/SkillEditor.vue` (add publish button). Use `ndf-check-disjoint.sh` after rebase to confirm no file overlap with other live features (per NDF Rule 12, Tier 3).

---

## §12 Performance Budgets

| Operation | p95 target | Notes |
|---|---|---|
| `POST /sanitize-preview` | < 3s | Dominated by qwen-turbo latency (typically 1-2s for <5KB body) |
| `POST /publish` | < 3.5s | Sanitize re-run + DB write |
| `GET /list` (with FULLTEXT) | < 200ms | ngram index + page_size ≤ 100 |
| `GET /:id` | < 50ms | Single-row lookup |
| `POST /:id/subscribe` | < 100ms | Two INSERTs + one UPDATE in tx |
| `DELETE /:id/unsubscribe` | < 100ms | One UPDATE + one DELETE in tx |

---

## §13 Open Decisions Resolved in S2

From S0 §7:

1. **Sanitize LLM model**: `qwen-turbo` start, monitor Langfuse for miss rate → upgrade to `qwen-plus` if needed
2. **Sanitize failure**: synchronous single-call; user-facing operation, no async retry (poor UX)
3. **Uniqueness key**: biz-layer enforced on `(source_skill_id, is_public=1)` — see D-UNIQ
4. **Subscription provenance**: enrich cloned skill description with `[订阅自市场 / marketplace_id=X / 日期 Y]`
5. **Recommendation sort**: `is_platform_recommended DESC, subscribe_count DESC, created_at DESC`
6. **subscribe_count consistency**: real-time +1/-1 in transaction; weekly cron reconcile out of scope (low-frequency op, manual fix sufficient if drift)
7. **Diff view library**: `vue-diff` (lightweight, no deps)
8. **Marketplace item delete semantics**: soft unpublish (`is_public=0`) only; hard delete reserved for admin emergency

---

## §14 S5 Validation Strategy (for S3 to elaborate)

The validation tasks for S5 (per NDF rule 10):

1. **Backend unit tests**:
   - `biz/marketplace/*_test.go` covering all 9 service methods
   - Cross-tenant isolation tests (A vs B scenarios)
   - Sanitize failure modes (LLM down, regex-only fallback)
   - Subscribe transaction atomicity (mock store mid-fail)
2. **Backend integration**: `go test ./internal/numind/store/...marketplace_test.go` with SQLite in-memory
3. **Lint/typecheck**: `task lint` + `npm run lint && npm run type-check`
4. **Playwright E2E** (`numind-web-v3/e2e/marketplace.spec.ts`):
   - Login as parent A → publish skill → assert marketplace row
   - Logout, login as parent B → browse marketplace → see A's item
   - Subscribe → verify cloned skill in B's `/config/skills`
   - Unsubscribe → verify cloned skill softdeleted
   - Login as child account → assert 403 on `/v1/marketplace/list`
5. **gstack /qa**: visual QA on browse / detail / publish diff view / subscribed table
6. **Langfuse trace verification**: trigger one publish, manually inspect Langfuse dashboard for `skill-marketplace-publish` trace + `sanitize-skill-body` generation

**Regression protection**: keep all unit + E2E tests in the codebase; choose Playwright E2E over gstack /qa for AC-1..AC-4 (cross-tenant flows have high regression risk; deserve persistent test coverage per memory `feedback_review_each_stage`).

---

## §15 Plan Atomicity Hint (for S3)

Suggested task decomposition (S3 writing-plans will refine):

1. **T1**: DB migrations + GORM models + AutoMigrate registration (numind-server)
2. **T2**: Store layer (`store/marketplace.go`) + unit tests
3. **T3**: `biz/marketplace/sanitize.go` (regex + LLM stages) + unit tests
4. **T4**: `biz/marketplace/service.go` (orchestration) + transaction handling + unit tests (cross-tenant scenarios)
5. **T5**: `biz/marketplace/clone.go` + `search.go` + tests
6. **T6**: Controller + admin controller + router registration
7. **T7**: errno additions
8. **T8**: Frontend api/store (numind-web-v3)
9. **T9**: Frontend views (Browse, Detail, Subscribed, Publish with diff)
10. **T10**: SkillEditor button + SkillList badge + AppLayout menu + router guards
11. **T11**: Playwright E2E (marketplace.spec.ts)
12. **T12**: S5 validation strategy (per Rule 10, this is a separate task in plan)

Tier protocol: T1-T7 in numind-server worktree, T8-T11 in numind-web-v3 worktree. T1→T2→T3→T4 strict serial (each builds on prior). T5-T7 can parallel within Tier 3 (different files in same repo, validated via `ndf-check-disjoint`). T8-T10 must follow corresponding backend APIs being committable.

---

## §16 Spec Sign-Off

| Item | Status |
|---|---|
| All PRD user stories covered by §3-8 | ✓ |
| API contracts defined for all 7+1 endpoints | ✓ |
| Trace topology defined for all LLM/cross-tenant operations | ✓ |
| Cross-tenant security analysis included | ✓ |
| Coordination with v2 #1 documented | ✓ |
| Performance budgets stated | ✓ |
| S5 validation strategy outlined for S3 | ✓ |
| S0 open decisions resolved | ✓ |

Per NDF autopilot rules, S2 gate is auto-approved (父账户已默认通过 v2 三件套). Proceed to S3.
