package wecom

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ImportService struct {
	db *gorm.DB
}

func NewImportService(db *gorm.DB) *ImportService {
	// Auto Migrate the tables
	_ = db.AutoMigrate(&ImportBatch{}, &ImportMessage{})
	return &ImportService{db: db}
}

func (s *ImportService) CreateImportBatch(userID int64, title string, messages []ImportMessage) (*ImportBatch, error) {
	// Calculate distinct speakers
	speakerSet := make(map[string]struct{})
	for _, m := range messages {
		if m.Speaker != "" {
			speakerSet[m.Speaker] = struct{}{}
		}
	}
	speakers := make([]string, 0, len(speakerSet))
	for k := range speakerSet {
		speakers = append(speakers, k)
	}
	sort.Strings(speakers) // Ensure deterministic order for session grouping
	speakersStr := strings.Join(speakers, ",")

	batch := &ImportBatch{
		ID:        uuid.New().String(),
		UserID:    userID,
		Title:     title,
		Speakers:  speakersStr, // Cache the speakers
		Status:    ImportBatchStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(batch).Error; err != nil {
			return err
		}

		for i := range messages {
			messages[i].BatchID = batch.ID
			messages[i].CreatedAt = time.Now()
		}

		if len(messages) > 0 {
			if err := tx.Create(&messages).Error; err != nil {
				return err
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return batch, nil
}

// ArchiveSession represents a grouped session in the UI
type ArchiveSession struct {
	SessionID  string `json:"session_id"`
	Title      string `json:"title"`
	LastActive string `json:"last_active"`
	MsgCount   int    `json:"msg_count"`
	Type       string `json:"type"` // "customer" or "system"
}

// GetArchiveSessions rewritten to split by MsgType
func (s *ImportService) GetArchiveSessions(userID int64) ([]ArchiveSession, error) {
	// We need all messages for this user.
	// Assuming messages are linked via Batch.
	var messages []struct {
		MessageID uint      `gorm:"column:message_id"`
		Content   string    `gorm:"column:content"`
		MsgType   string    `gorm:"column:msg_type"`
		CreatedAt time.Time `gorm:"column:created_at"`
		Title     string    `gorm:"column:title"` // Batch Title
	}

	// Fetch essential fields
	err := s.db.Table("import_messages").
		Select("import_messages.id as message_id, import_messages.content, import_messages.msg_type, import_messages.created_at, import_batches.title").
		Joins("JOIN import_batches ON import_batches.id = import_messages.batch_id").
		Where("import_batches.user_id = ?", userID).
		Order("import_messages.msg_time desc"). // Show newest first? User said Order by CreatedAt previously? Let's use MsgTime for chat order logic usually. But CreatedAt for archive list is ok.
		Scan(&messages).Error

	if err != nil {
		return nil, err
	}

	var result []ArchiveSession

	// Classify
	myHistoryCount := 0
	var myHistoryLastActive time.Time
	hasMyHistory := false

	for _, m := range messages {
		// Strict check based on MsgType
		if m.MsgType == "chatrecord" {
			// Distinct Session for this merged record
			sessionID := fmt.Sprintf("msg_%d", m.MessageID)

			// For title: The chatrecord content usually contains a title in JSON
			// But doing full parse here is heavy.
			// Let's use the Batch title as a base, or "合并聊天记录"
			displayTitle := m.Title
			if displayTitle == "" {
				displayTitle = "合并聊天记录"
			}

			// Try simple string find for title in Content content if possible?
			// {"chatrecord":{"title":"Leo和pmtm啊的聊天记录"...
			// Let's rely on Batch Title for now as it's cleaner.

			result = append(result, ArchiveSession{
				SessionID:  sessionID,
				Title:      displayTitle,
				LastActive: m.CreatedAt.Format(time.RFC3339),
				MsgCount:   1, // It is ONE record representing a session
				Type:       "customer",
			})

		} else {
			// Aggregated Messages (text, image, etc. sent directly to Bot)
			myHistoryCount++
			if !hasMyHistory || m.CreatedAt.After(myHistoryLastActive) {
				myHistoryLastActive = m.CreatedAt
				hasMyHistory = true
			}
		}
	}

	// Add My History
	if !hasMyHistory {
		myHistoryLastActive = time.Now()
	}

	mySession := ArchiveSession{
		SessionID:  "myself",
		Title:      "个人记录 (My History)",
		LastActive: myHistoryLastActive.Format(time.RFC3339),
		MsgCount:   myHistoryCount,
		Type:       "system",
	}
	// Prepend
	result = append([]ArchiveSession{mySession}, result...)

	return result, nil
}

// GetSessionMessages rewritten to expand chatrecord content
func (s *ImportService) GetSessionMessages(userID int64, sessionKey string) ([]ImportMessage, error) {
	if sessionKey == "myself" {
		// Return all messages that are NOT chatrecord
		var allMessages []ImportMessage
		err := s.db.Table("import_messages").
			Select("import_messages.*").
			Joins("JOIN import_batches ON import_batches.id = import_messages.batch_id").
			Where("import_batches.user_id = ? AND import_messages.msg_type != ?", userID, "chatrecord").
			Order("import_messages.msg_time asc").
			Find(&allMessages).Error

		return allMessages, err

	} else if strings.HasPrefix(sessionKey, "msg_") {
		// Single ChatRecord Expansion
		msgIDStr := strings.TrimPrefix(sessionKey, "msg_")

		var msg ImportMessage
		err := s.db.Table("import_messages").
			Select("import_messages.*").
			Joins("JOIN import_batches ON import_batches.id = import_messages.batch_id").
			Where("import_batches.user_id = ? AND import_messages.id = ?", userID, msgIDStr).
			First(&msg).Error

		if err != nil {
			return nil, err
		}

		// Expand Content
		// Content expected to be JSON of merged history
		var rawMap map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &rawMap); err != nil {
			return []ImportMessage{msg}, nil // Fallback
		}

		// Find items
		var items []interface{}
		// Structure 1: {"chatrecord": {"item": [...]}}
		// Structure 2: {"title": "...", "item": [...]} (if content IS the chatrecord object)

		if cr, ok := rawMap["chatrecord"].(map[string]interface{}); ok {
			if it, ok := cr["item"].([]interface{}); ok {
				items = it
			}
		} else if it, ok := rawMap["item"].([]interface{}); ok {
			// Maybe root is the record
			items = it
		} else {
			// scan
			for _, v := range rawMap {
				if subMap, ok := v.(map[string]interface{}); ok {
					if cr, ok := subMap["chatrecord"].(map[string]interface{}); ok {
						if it, ok := cr["item"].([]interface{}); ok {
							items = it
							break
						}
					}
				}
			}
		}

		if items == nil {
			// No items found -> return raw (maybe just empty or wrong format)
			return []ImportMessage{msg}, nil
		}

		var expanded []ImportMessage
		for _, it := range items {
			itemMap, ok := it.(map[string]interface{})
			if !ok {
				continue
			}

			mType, _ := itemMap["type"].(string)

			// Content extraction
			mContent := ""
			if c, ok := itemMap["content"].(string); ok {
				mContent = c
			}

			// Nested JSON string parsing for text
			if mType == "ChatRecordText" {
				var txtObj struct {
					Content string `json:"content"`
				}
				if json.Unmarshal([]byte(mContent), &txtObj) == nil && txtObj.Content != "" {
					mContent = txtObj.Content
				}
				mType = "text"
			} else if mType == "ChatRecordImage" {
				mType = "image"
				mContent = "[图片]"
			} else if mType == "ChatRecordVoice" {
				mType = "voice"
				mContent = "[语音]"
			} else if mType == "ChatRecordVideo" {
				mType = "video"
				mContent = "[视频]"
			} else if mType == "ChatRecordFile" {
				mType = "file"
				mContent = "[文件]"
			}

			if mType == "" {
				mType = "text"
			}

			var mTime int64
			if tVal, ok := itemMap["msgtime"].(float64); ok {
				mTime = int64(tVal) * 1000
			}

			mSpeaker, _ := itemMap["sourcename"].(string)

			expanded = append(expanded, ImportMessage{
				ID:        0,
				BatchID:   msg.BatchID,
				MsgTime:   mTime,
				Speaker:   mSpeaker,
				Content:   mContent,
				MsgType:   mType,
				CreatedAt: time.Now(),
			})
		}
		return expanded, nil

	}

	return []ImportMessage{}, nil
}
