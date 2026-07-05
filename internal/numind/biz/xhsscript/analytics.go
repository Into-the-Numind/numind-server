package xhsscript

import (
	"context"
	"strings"
	"time"

	"numind-server/internal/pkg/model"
)

type AnalyticsEventInput struct {
	EventID     string                 `json:"event_id"`
	EventName   string                 `json:"event_name"`
	AnonymousID string                 `json:"anonymous_id"`
	SessionID   string                 `json:"session_id"`
	Path        string                 `json:"path"`
	Properties  map[string]interface{} `json:"properties"`
	OccurredAt  string                 `json:"occurred_at"`
}

func (s *Service) TrackEvents(ctx context.Context, userID *uint, events []AnalyticsEventInput) error {
	for _, input := range events {
		eventID := strings.TrimSpace(input.EventID)
		eventName := strings.TrimSpace(input.EventName)
		if eventID == "" || eventName == "" {
			continue
		}
		createdAt := time.Now()
		if input.OccurredAt != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, input.OccurredAt); err == nil {
				createdAt = parsed
			}
		}
		event := &model.XhsScriptAnalyticsEvent{
			EventID:     eventID,
			EventName:   eventName,
			AnonymousID: strings.TrimSpace(input.AnonymousID),
			UserID:      userID,
			SessionID:   strings.TrimSpace(input.SessionID),
			Path:        strings.TrimSpace(input.Path),
			Properties:  mustJSON(input.Properties),
			CreatedAt:   createdAt,
		}
		if err := s.ds.XhsScript().InsertAnalyticsEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
