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

// GetArchiveSessions rewritten to filter by Content pattern
func (s *ImportService) GetArchiveSessions(userID int64) ([]ArchiveSession, error) {
	// Join with Batch to filter by UserID
	var messages []struct {
		MessageID uint      `gorm:"column:message_id"`
		BatchID   string    `gorm:"column:batch_id"`
		Content   string    `gorm:"column:content"`
		CreatedAt time.Time `gorm:"column:created_at"`
		Title     string    `gorm:"column:title"` // Batch Title as fallback
		Speaker   string    `gorm:"column:speaker"`
	}

	err := s.db.Table("import_messages").
		Select("import_messages.id as message_id, import_messages.batch_id, import_messages.content, import_messages.created_at, import_messages.speaker, import_batches.title").
		Joins("JOIN import_batches ON import_batches.id = import_messages.batch_id").
		Where("import_batches.user_id = ?", userID).
		Order("import_messages.created_at desc").
		Scan(&messages).Error

	if err != nil {
		return nil, err
	}

	var result []ArchiveSession

	myHistoryCount := 0
	var myHistoryLastActive time.Time
	hasMyHistory := false

	for _, m := range messages {
		// Pattern Check: Starts with {"msgid" AND Ends with }]}}
		isMergedRecord := strings.HasPrefix(m.Content, `{"msgid"`) && strings.HasSuffix(m.Content, `}]}}`)

		if !isMergedRecord {
			// "Personal Record" / Single Messages
			myHistoryCount++
			if !hasMyHistory || m.CreatedAt.After(myHistoryLastActive) {
				myHistoryLastActive = m.CreatedAt
				hasMyHistory = true
			}
		} else {
			// Distinct Session for Merged Record
			// We use the MessageID as basis for SessionID.
			// Format: "msg_<ID>"
			sessionID := fmt.Sprintf("msg_%d", m.MessageID)

			// Try to extract title from content or use Batch title fallback
			// For now, simpler to use Batch Title or a generic name.
			displayTitle := m.Title
			if displayTitle == "" {
				displayTitle = "合并聊天记录"
			}
			if m.Speaker != "" {
				// Maybe incorporate speaker name
			}

			result = append(result, ArchiveSession{
				SessionID:  sessionID,
				Title:      displayTitle,
				LastActive: m.CreatedAt.Format(time.RFC3339),
				MsgCount:   1,          // Represents 1 merged message record
				Type:       "customer", // Merged records are customer records
			})
		}
	}

	// Add My History (Personal Record)
	// Always adding it even if empty, as per user request "Personal Record session forever exists"

	// Default Time if none
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

// GetSessionMessages rewritten to handle virtual expansion of merged records
func (s *ImportService) GetSessionMessages(userID int64, sessionKey string) ([]ImportMessage, error) {
	if sessionKey == "myself" {
		// Return all messages that are NOT merged records
		// Ideally we do this in SQL: content NOT LIKE '{"msgid" ...'
		// But the pattern is long.
		// Let's filter in Go for consistency with GetArchiveSessions,
		// OR better: use SQL LIKE logic if possible.
		// strings.HasPrefix check in SQL is `content LIKE '{"msgid"%'`

		var allMessages []ImportMessage
		// Join with Batch to filter by user.
		// We only select the fields we need to return.
		err := s.db.Table("import_messages").
			Select("import_messages.*").
			Joins("JOIN import_batches ON import_batches.id = import_messages.batch_id").
			Where("import_batches.user_id = ?", userID).
			Order("import_messages.msg_time asc").
			Find(&allMessages).Error

		if err != nil {
			return nil, err
		}

		// Filter valid messages (exclude merged chunks)
		var filtered []ImportMessage
		for _, m := range allMessages {
			isMergedRecord := strings.HasPrefix(m.Content, `{"msgid"`) && strings.HasSuffix(m.Content, `}]}}`)
			if !isMergedRecord {
				filtered = append(filtered, m)
			}
		}
		return filtered, nil

	} else if strings.HasPrefix(sessionKey, "msg_") {
		// Single Merged Message Expansion
		msgIDStr := strings.TrimPrefix(sessionKey, "msg_")

		// Find the single message
		var msg ImportMessage
		// Join purely for User check safety (though msg ID is unique)
		err := s.db.Table("import_messages").
			Select("import_messages.*").
			Joins("JOIN import_batches ON import_batches.id = import_messages.batch_id").
			Where("import_batches.user_id = ? AND import_messages.id = ?", userID, msgIDStr).
			First(&msg).Error

		if err != nil {
			return nil, err
		}

		// Now we must expand this message content into a list of messages.
		// Expected Content format: `{"msgid":123, "chatrecord":{...}}` or similar raw structure?
		// Wait, user said `{"msgid"` start.
		// We need to parse this JSON.
		// Let's assume a generic structure compatible with what we saw in Frontend.
		// JSON structure likely:
		// { ... "chatrecord": { "title": "xxx", "item": [ ... ] } ... }
		// Or the root IS the item list wrapper?
		// Let's define a helper struct to parse.

		type ChatRecordItem struct {
			Type         string `json:"type"`
			Content      string `json:"content"` // Can be string or nested JSON string
			MsgTime      int64  `json:"msgtime"`
			SourceNameTo string `json:"sourcename"`
			// ... other fields
		}

		type ChatRecordWrapper struct {
			ChatRecord struct {
				Title string           `json:"title"`
				Item  []ChatRecordItem `json:"item"`
			} `json:"chatrecord"`
		}

		// Try unmarshal
		// If the JSON is wrapped in external layers (like the user implied with {"msgid"...}),
		// we might need to parse it generically or find the 'chatrecord' key.
		// Let's try direct unmarshal first (if Content contains chatrecord key).

		// If simple unmarshal fails, we might need to try partial.
		// Given Go's JSON parser, strict structure is needed.
		// Let's use map[string]interface{} for safety if structure varies.

		var rawMap map[string]interface{}
		if err := json.Unmarshal([]byte(msg.Content), &rawMap); err != nil {
			// Fallback: return the original message as is (failed to expand)
			return []ImportMessage{msg}, nil
		}

		// Locate "chatrecord"
		var items []interface{}
		// Check root first
		if cr, ok := rawMap["chatrecord"].(map[string]interface{}); ok {
			if it, ok := cr["item"].([]interface{}); ok {
				items = it
			}
		} else {
			// Maybe nested? Only searching 1 level deep for now.
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

		// Convert items to ImportMessages
		var expanded []ImportMessage

		for _, it := range items {
			itemMap, ok := it.(map[string]interface{})
			if !ok {
				continue
			}

			// Extract fields
			// type
			mType, _ := itemMap["type"].(string)
			// content: might be JSON string inside.
			// user wants "normal bubble", so we keep it as string.
			mContent := ""
			if c, ok := itemMap["content"].(string); ok {
				mContent = c
			}

			// Check if content is inner JSON (ChatRecordText)
			// Frontend usually handles this, OR we parse it here to be cleaner.
			// "ChatRecordText" usually has content `{"content":"Actual Text"}`
			if mType == "ChatRecordText" {
				// try to peek inside to get plain text?
				// Doing simplistic extraction:
				var txtObj struct {
					Content string `json:"content"`
				}
				if json.Unmarshal([]byte(mContent), &txtObj) == nil && txtObj.Content != "" {
					mContent = txtObj.Content
					mType = "text" // Normalize type for frontend
				} else {
					mType = "text"
				}
			} else if mType == "ChatRecordImage" {
				mType = "image"
				mContent = "[图片]" // Placeholders for now until media logic
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

			// time
			var mTime int64
			if tVal, ok := itemMap["msgtime"].(float64); ok {
				mTime = int64(tVal) * 1000 // verify units. chatrecord usually seconds? Frontend code said *1000.
			}

			// speaker
			mSpeaker, _ := itemMap["sourcename"].(string)

			// Create message
			expanded = append(expanded, ImportMessage{
				ID:        0, // Virtual ID
				BatchID:   msg.BatchID,
				MsgTime:   mTime,
				Speaker:   mSpeaker,
				Content:   mContent,
				MsgType:   mType,
				CreatedAt: time.Now(),
			})
		}

		return expanded, nil

	} else {
		// Fallback for old SessionID style (batch ID) just in case
		// Verify it belongs to user
		var batch ImportBatch
		if err := s.db.Where("id = ? AND user_id = ?", sessionKey, userID).First(&batch).Error; err != nil {
			return []ImportMessage{}, nil
		}
		var targetBatches []string
		targetBatches = append(targetBatches, batch.ID)

		var messages []ImportMessage
		if err := s.db.Where("batch_id IN ?", targetBatches).Order("msg_time asc").Find(&messages).Error; err != nil {
			return nil, err
		}
		return messages, nil
	}
}
