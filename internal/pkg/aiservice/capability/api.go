package capability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/log"
)

// packageDB is the gorm.DB injected at startup via Init.
// It must be set before any calls to GetCapabilities / CanAcceptModality /
// ResolveFallbackBehavior.
var packageDB *gorm.DB

// Init sets the DB used for capability lookups. Call this once at startup
// (typically alongside registry.New(db)).
//
//	capability.Init(store.S.DB())
func Init(db *gorm.DB) {
	packageDB = db
}

// GetCapabilities returns the Capabilities for the given modelKey.
//
// Lookup order:
//  1. In-memory cache (5-min TTL).
//  2. ai_service DB row (by model_key, deprecated_at IS NULL).
//  3. Conservative defaults + ErrModelNotFound when the key is unknown.
//
// capability_json parse errors are logged at WARN and fall back to the
// conservative default (backward-compatible with old rows that have no
// capability_json or a partial document).
//
// TODO(task-1.3): Accept ctx context.Context parameter so DB lookups can be
// cancelled when the parent request times out. Currently uses context.Background()
// internally (via lookupCapabilities). Change signature to
// GetCapabilities(ctx context.Context, modelKey string) when task 1.3 integrates
// capability checks into the agent request path.
func GetCapabilities(modelKey string) (*Capabilities, error) {
	// 1. Cache hit.
	if caps, ok := cacheGet(modelKey); ok {
		return caps, nil
	}

	// 2. DB lookup.
	caps, err := lookupCapabilities(modelKey)
	if err != nil {
		// Return conservative defaults + the error; caller decides to reject or degrade.
		c := defaultConservative
		return &c, err
	}

	// 3. Populate cache (even for empty capability_json rows — those get conservative defaults).
	cacheSet(modelKey, caps)
	return caps, nil
}

// CanAcceptModality reports whether the model identified by modelKey can
// receive the given mediaType as an inline attachment.
//
// Returns (false, ErrModelNotFound) when the model is unknown.
// Returns (false, nil) when the model is known but does not support that mediaType.
func CanAcceptModality(modelKey string, mediaType MediaType) (bool, error) {
	caps, err := GetCapabilities(modelKey)
	if err != nil {
		return false, err
	}
	return caps.acceptsModality(mediaType), nil
}

// ResolveFallbackBehavior returns the FallbackPolicy for the given modelKey and mediaType.
//
// Decision matrix:
//
//	AcceptsInline | mediaType | policy
//	true          | image     | FallbackInline
//	true          | pdf       | FallbackInline
//	false         | image     | FallbackToText   (vision_description + ocr_text)
//	false         | pdf       | FallbackToOCROnly (only ocr_text)
//	any           | audio     | FallbackReject   (V1.5: no audio fallback)
//
// When the model is unknown (ErrModelNotFound), conservative defaults apply
// (AcceptsInline = false for all media types).
func ResolveFallbackBehavior(modelKey string, mediaType MediaType) FallbackPolicy {
	caps, err := GetCapabilities(modelKey)
	if err != nil {
		// Unknown model: apply conservative routing.
		log.Warnw("capability.ResolveFallbackBehavior: model not found, using conservative defaults",
			"model_key", modelKey, "media_type", mediaType, "error", err)
	}
	return resolvePolicyFromCaps(caps, mediaType)
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// acceptsModality returns true if caps indicate the model accepts the given mediaType inline.
func (c *Capabilities) acceptsModality(mediaType MediaType) bool {
	switch mediaType {
	case MediaImage:
		return c.AcceptsImageInline
	case MediaPDF:
		return c.AcceptsPDFInline
	case MediaAudio:
		return c.AcceptsAudioInline
	default:
		return false
	}
}

// resolvePolicyFromCaps translates Capabilities + MediaType into a FallbackPolicy.
func resolvePolicyFromCaps(caps *Capabilities, mediaType MediaType) FallbackPolicy {
	switch mediaType {
	case MediaAudio:
		// V1.5: no audio fallback pipeline exists.
		return FallbackReject

	case MediaPDF:
		if caps.AcceptsPDFInline {
			return FallbackInline
		}
		return FallbackToOCROnly

	case MediaImage:
		if caps.AcceptsImageInline {
			return FallbackInline
		}
		return FallbackToText

	default:
		// Unknown media type: conservatively reject.
		return FallbackReject
	}
}

// lookupCapabilities queries the DB for a single ai_service row by model_key
// and parses its capability_json into a Capabilities struct.
//
// Returns:
//   - (*Capabilities, nil)       — found and parsed (possibly empty JSON → defaults).
//   - (defaults, ErrModelNotFound) — no non-deprecated row for model_key.
//   - (defaults, fmt.Errorf(...))  — DB error.
func lookupCapabilities(modelKey string) (*Capabilities, error) {
	if packageDB == nil {
		// packageDB not initialised — behave as if DB returned no row.
		c := defaultConservative
		return &c, fmt.Errorf("capability: DB not initialised (call capability.Init first)")
	}

	type rawRow struct {
		CapabilityJSONStr *string `gorm:"column:capability_json"`
	}
	var row rawRow
	err := packageDB.WithContext(context.Background()).
		Table("ai_service").
		Select("capability_json").
		Where("model_key = ? AND deprecated_at IS NULL", modelKey).
		First(&row).Error

	if err != nil {
		if isNotFound(err) {
			c := defaultConservative
			return &c, ErrModelNotFound
		}
		c := defaultConservative
		return &c, fmt.Errorf("capability.lookupCapabilities(%q): %w", modelKey, err)
	}

	// Empty capability_json → return conservative defaults (backward-compatible).
	if row.CapabilityJSONStr == nil || *row.CapabilityJSONStr == "" || *row.CapabilityJSONStr == "null" {
		c := defaultConservative
		return &c, nil
	}

	var caps Capabilities
	if err := json.Unmarshal([]byte(*row.CapabilityJSONStr), &caps); err != nil {
		log.Warnw("capability.lookupCapabilities: capability_json parse error, using conservative defaults",
			"model_key", modelKey, "error", err)
		c := defaultConservative
		return &c, nil
	}

	// Ensure PreferredImageFormat has a safe default.
	if caps.PreferredImageFormat == "" {
		caps.PreferredImageFormat = "base64"
	}

	return &caps, nil
}

// isNotFound reports whether err is a GORM record-not-found error.
func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
