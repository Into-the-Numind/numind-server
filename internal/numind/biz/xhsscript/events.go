package xhsscript

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

const backendEventIDPrefix = "backend:xhs_script"

// RecordEvent stores a backend-authored XHS script analytics event.
func (s *Service) RecordEvent(ctx context.Context, userID uint, eventName string, properties map[string]interface{}) error {
	return s.RecordEventWithID(ctx, userID, "", eventName, properties)
}

// RecordEventWithID stores a backend-authored XHS script analytics event using
// a caller-provided event ID for idempotent product flows.
func (s *Service) RecordEventWithID(ctx context.Context, userID uint, eventID, eventName string, properties map[string]interface{}) error {
	userIDCopy := userID
	return s.recordEvent(ctx, &userIDCopy, eventID, eventName, properties)
}

// RecordEventBestEffort records an analytics event and logs failures without
// changing the caller's product flow.
func (s *Service) RecordEventBestEffort(ctx context.Context, userID uint, eventName string, properties map[string]interface{}) {
	s.RecordEventWithIDBestEffort(ctx, userID, "", eventName, properties)
}

// RecordEventWithIDBestEffort records an idempotent analytics event and logs
// failures without changing the caller's product flow.
func (s *Service) RecordEventWithIDBestEffort(ctx context.Context, userID uint, eventID, eventName string, properties map[string]interface{}) {
	if err := s.RecordEventWithID(ctx, userID, eventID, eventName, properties); err != nil {
		log.Warnw("xhs-script analytics event failed", "event_name", eventName, "event_id", eventID, "user_id", userID, "error", err)
	}
}

func (s *Service) recordEvent(ctx context.Context, userID *uint, eventID, eventName string, properties map[string]interface{}) error {
	eventName = strings.TrimSpace(eventName)
	if eventName == "" {
		return fmt.Errorf("xhs script analytics event name is required")
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		eventID = newBackendEventID(eventName)
	}
	if properties == nil {
		properties = map[string]interface{}{}
	}
	event := &model.XhsScriptAnalyticsEvent{
		EventID:    eventID,
		EventName:  eventName,
		UserID:     userID,
		Properties: mustJSON(sanitizeAnalyticsProperties(properties)),
		CreatedAt:  time.Now(),
	}
	return s.ds.XhsScript().InsertAnalyticsEvent(ctx, event)
}

func newBackendEventID(eventName string) string {
	eventName = strings.ReplaceAll(strings.TrimSpace(eventName), " ", "_")
	if eventName == "" {
		eventName = "event"
	}
	runes := []rune(eventName)
	if len(runes) > 43 {
		eventName = string(runes[:43])
	}
	return fmt.Sprintf("%s:%s:%s", backendEventIDPrefix, eventName, uuid.NewString())
}

func analyticsErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	switch {
	case errors.Is(err, errno.ErrXhsScriptNoteNotFound):
		return "not_found"
	case errors.Is(err, errno.ErrXhsScriptVideoOnly):
		return "video_only"
	case errors.Is(err, errno.ErrXhsScriptTranscriptNotReady):
		return "transcript_not_ready"
	case errors.Is(err, errno.ErrXhsScriptProfileRequired):
		return "profile_required"
	case errors.Is(err, errno.ErrXhsScriptQuotaInsufficient), errors.Is(err, store.ErrXhsScriptQuotaInsufficient):
		return "quota_insufficient"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "record not found"):
		return "not_found"
	case strings.Contains(msg, "视频地址为空"):
		return "video_url_empty"
	case strings.Contains(msg, "http "):
		return "http_status"
	case strings.Contains(msg, "大小限制"):
		return "video_too_large"
	case strings.Contains(msg, "时长超过限制"):
		return "video_too_long"
	case strings.Contains(msg, "ffmpeg"):
		return "audio_extract_failed"
	case strings.Contains(msg, "read extracted audio"):
		return "audio_read_failed"
	case strings.Contains(msg, "asr transcript empty"):
		return "transcript_empty"
	case strings.Contains(msg, "asr"):
		return "asr_failed"
	case strings.Contains(msg, "quota"):
		return "quota"
	case strings.Contains(msg, "profile"):
		return "profile"
	default:
		return "unknown"
	}
}

func mergeAnalyticsProperties(base map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func textLength(value string) int {
	return len([]rune(strings.TrimSpace(value)))
}
