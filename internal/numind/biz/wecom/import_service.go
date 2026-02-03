package wecom

import (
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

func (s *ImportService) GetArchiveSessions(userID int64) ([]ArchiveSession, error) {
	var batches []ImportBatch
	// Pending status check removed; we show all imported history regardless of "status"
	// effectively "import" IS "archive" now.
	if err := s.db.Where("user_id = ?", userID).Order("created_at desc").Find(&batches).Error; err != nil {
		return nil, err
	}

	// Group by Speakers
	sessionMap := make(map[string]*ArchiveSession)

	for _, b := range batches {
		speakers := b.Speakers
		if speakers == "" {
			speakers = "myself" // Fallback key
		}

		sessionKey := speakers
		// MD5/Hash the key for ID safety if needed, but raw string is OK for map key

		// Determine Type
		sessType := "customer"
		participants := strings.Split(speakers, ",")
		if len(participants) < 2 {
			sessType = "system"   // My Sent History / Monologue
			sessionKey = "myself" // Force all single-party/zero-party into one bucket
		}

		if _, exists := sessionMap[sessionKey]; !exists {
			title := speakers
			if sessType == "system" {
				title = "个人记录 (My History)"
			}

			sessionMap[sessionKey] = &ArchiveSession{
				SessionID:  sessionKey, // Use speakers string as ID (simple & stable)
				Title:      title,
				LastActive: b.CreatedAt.Format(time.RFC3339), // Most recent batch is first due to sort
				MsgCount:   0,
				Type:       sessType,
			}
		}
		// Aggregate counts (optional, might be expensive to be precise, just store count of batches?)
		sessionMap[sessionKey].MsgCount++
	}

	var result []ArchiveSession
	for _, sess := range sessionMap {
		result = append(result, *sess)
	}

	// Sort: System first, then by LastActive
	sort.Slice(result, func(i, j int) bool {
		if result[i].Type == "system" {
			return true
		}
		if result[j].Type == "system" {
			return false
		}
		return result[i].LastActive > result[j].LastActive
	})

	return result, nil
}

// GetSessionMessages fetches all messages belonging to a 'session' (speaker set)
func (s *ImportService) GetSessionMessages(userID int64, sessionKey string) ([]ImportMessage, error) {
	var targetBatches []string

	if sessionKey == "myself" {
		// Find all single-party batches
		// Doing a query for this is tricky with string exact match.
		// We can fetch all and filter in memory since batch count is usually manageable,
		// OR strictly match the exact speaker strings if we knew them.
		// For simplicity/performance balance: Fetch all batches for user, filter again.
		// Optimized approach: SQL LIKE query? No, speakers variable.

		// Let's fetch all batches first.
		var allBatches []ImportBatch
		if err := s.db.Where("user_id = ?", userID).Find(&allBatches).Error; err != nil {
			return nil, err
		}
		for _, b := range allBatches {
			parts := strings.Split(b.Speakers, ",")
			if len(parts) < 2 {
				targetBatches = append(targetBatches, b.ID)
			}
		}
	} else {
		// Exact match on logic key (speakers string)
		// Note: sessionKey comes from frontend, which came from our GetArchiveSessions logic.
		// It IS the raw speakers string (e.g., "Kevin,Li").
		var batches []ImportBatch
		if err := s.db.Where("user_id = ? AND speakers = ?", userID, sessionKey).Find(&batches).Error; err != nil {
			return nil, err
		}
		for _, b := range batches {
			targetBatches = append(targetBatches, b.ID)
		}
	}

	if len(targetBatches) == 0 {
		return []ImportMessage{}, nil
	}

	var messages []ImportMessage
	if err := s.db.Where("batch_id IN ?", targetBatches).Order("msg_time asc").Find(&messages).Error; err != nil {
		return nil, err
	}

	return messages, nil
}
