package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/pkg/model"
)

var (
	ErrXhsScriptQuotaInsufficient = errors.New("xhs script quota insufficient")
	ErrXhsScriptQuotaRefRequired  = errors.New("xhs script quota ref id required")
)

type IXhsScriptStore interface {
	GetOrCreateProfileByUser(ctx context.Context, userID uint) (*model.XhsScriptUserProfile, error)
	SaveProfile(ctx context.Context, profile *model.XhsScriptUserProfile) error
	CreateOrGetQuotaAccount(ctx context.Context, userID uint) (*model.XhsScriptQuotaAccount, error)
	GetQuotaAccount(ctx context.Context, userID uint) (*model.XhsScriptQuotaAccount, error)
	AddPaidQuota(ctx context.Context, userID uint, amount int64, refID string) error
	InsertAnalyticsEvent(ctx context.Context, event *model.XhsScriptAnalyticsEvent) error
	CreateOrUpsertCapturedNote(ctx context.Context, note *model.XhsScriptNote) (*model.XhsScriptNote, error)
	GetNote(ctx context.Context, userID uint, id uint64) (*model.XhsScriptNote, error)
	ListNotes(ctx context.Context, userID uint, limit, offset int) ([]model.XhsScriptNote, error)
	UpdateNoteVideoURL(ctx context.Context, userID uint, id uint64, videoURL string) error
	UpdateTranscribeStatus(ctx context.Context, userID uint, id uint64, status string, transcript *string, lastError string) error
	UpdateGenerateStatus(ctx context.Context, userID uint, id uint64, status string, lastError string) error
	CreateGeneration(ctx context.Context, userID uint, noteID uint64, scriptText string, promptTokens, completionTokens int) (*model.XhsScriptGeneration, error)
	CreateGenerationAndDeductQuota(ctx context.Context, userID uint, noteID uint64, scriptText string, promptTokens, completionTokens int) (*XhsScriptGenerationCommit, error)
	DeductOneGeneration(ctx context.Context, userID uint, refID string) error
}

type XhsScriptGenerationCommit struct {
	Generation *model.XhsScriptGeneration
	Bucket     string
	FreeBefore int64
	PaidBefore int64
}

type xhsScriptStore struct {
	db *gorm.DB
}

var _ IXhsScriptStore = (*xhsScriptStore)(nil)

func NewXhsScriptStore(db *gorm.DB) IXhsScriptStore {
	return &xhsScriptStore{db: db}
}

func (s *xhsScriptStore) GetOrCreateProfileByUser(ctx context.Context, userID uint) (*model.XhsScriptUserProfile, error) {
	profile := model.XhsScriptUserProfile{UserID: userID}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&profile).Error; err != nil {
			return fmt.Errorf("create profile: %w", err)
		}
		if err := tx.Where("user_id = ?", userID).First(&profile).Error; err != nil {
			return fmt.Errorf("load profile: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("GetOrCreateProfileByUser: %w", err)
	}
	return &profile, nil
}

func (s *xhsScriptStore) SaveProfile(ctx context.Context, profile *model.XhsScriptUserProfile) error {
	if profile == nil {
		return errors.New("SaveProfile: nil profile")
	}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"profile_text", "updated_at"}),
	}).Create(profile).Error; err != nil {
		return fmt.Errorf("SaveProfile: %w", err)
	}
	return nil
}

func (s *xhsScriptStore) CreateOrGetQuotaAccount(ctx context.Context, userID uint) (*model.XhsScriptQuotaAccount, error) {
	account := model.XhsScriptQuotaAccount{
		UserID:        userID,
		FreeRemaining: 3,
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&account).Error; err != nil {
			return fmt.Errorf("create account: %w", err)
		}
		if err := tx.Where("user_id = ?", userID).First(&account).Error; err != nil {
			return fmt.Errorf("load account: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("CreateOrGetQuotaAccount: %w", err)
	}
	return &account, nil
}

func (s *xhsScriptStore) GetQuotaAccount(ctx context.Context, userID uint) (*model.XhsScriptQuotaAccount, error) {
	var account model.XhsScriptQuotaAccount
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&account).Error; err != nil {
		return nil, fmt.Errorf("GetQuotaAccount: %w", err)
	}
	return &account, nil
}

func (s *xhsScriptStore) AddPaidQuota(ctx context.Context, userID uint, amount int64, refID string) error {
	if amount <= 0 {
		return errors.New("AddPaidQuota: amount must be positive")
	}
	if refID == "" {
		return ErrXhsScriptQuotaRefRequired
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ledger := model.XhsScriptQuotaLedger{
			UserID:  userID,
			Delta:   amount,
			Bucket:  model.XhsScriptQuotaBucketPaid,
			Reason:  model.XhsScriptLedgerReasonPurchase,
			RefType: model.XhsScriptLedgerRefTypePurchase,
			RefID:   refID,
		}
		inserted, err := insertQuotaLedgerOnce(ctx, tx, &ledger)
		if err != nil {
			return fmt.Errorf("create ledger: %w", err)
		}
		if !inserted {
			return nil
		}

		account, err := findOrCreateQuotaAccountForUpdate(ctx, tx, userID)
		if err != nil {
			return fmt.Errorf("quota account: %w", err)
		}
		if err := tx.Model(&model.XhsScriptQuotaAccount{}).
			Where("id = ?", account.ID).
			Updates(map[string]interface{}{
				"paid_remaining": account.PaidRemaining + amount,
				"updated_at":     time.Now(),
			}).Error; err != nil {
			return fmt.Errorf("update paid quota: %w", err)
		}
		return nil
	})
}

func (s *xhsScriptStore) InsertAnalyticsEvent(ctx context.Context, event *model.XhsScriptAnalyticsEvent) error {
	if event == nil {
		return errors.New("InsertAnalyticsEvent: nil event")
	}
	if err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(event).Error; err != nil {
		return fmt.Errorf("InsertAnalyticsEvent: %w", err)
	}
	return nil
}

func (s *xhsScriptStore) CreateOrUpsertCapturedNote(ctx context.Context, note *model.XhsScriptNote) (*model.XhsScriptNote, error) {
	if note == nil {
		return nil, errors.New("CreateOrUpsertCapturedNote: nil note")
	}
	if note.SourceNoteID == "" {
		return nil, errors.New("CreateOrUpsertCapturedNote: source_note_id is required")
	}
	applyCapturedNoteDefaults(note)

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.XhsScriptNote
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND source_note_id = ?", note.UserID, note.SourceNoteID).
			First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if cErr := tx.Create(note).Error; cErr != nil {
				if isDuplicateKeyErr(cErr) {
					return updateCapturedNoteAfterDuplicate(ctx, tx, note)
				}
				return fmt.Errorf("create: %w", cErr)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("lookup: %w", err)
		}
		note.ID = existing.ID
		updates := capturedNoteMetadataUpdateMap(note)
		preserveMirroredVideoURLOnRecapture(updates, existing.VideoURL)
		if err := tx.Model(&model.XhsScriptNote{}).Where("id = ?", existing.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update: %w", err)
		}
		return tx.First(note, existing.ID).Error
	})
	if err != nil {
		return nil, fmt.Errorf("CreateOrUpsertCapturedNote: %w", err)
	}
	return note, nil
}

func updateCapturedNoteAfterDuplicate(ctx context.Context, tx *gorm.DB, note *model.XhsScriptNote) error {
	var existing model.XhsScriptNote
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND source_note_id = ?", note.UserID, note.SourceNoteID).
		First(&existing).Error; err != nil {
		return fmt.Errorf("reload after duplicate: %w", err)
	}
	note.ID = existing.ID
	updates := capturedNoteMetadataUpdateMap(note)
	preserveMirroredVideoURLOnRecapture(updates, existing.VideoURL)
	if err := tx.Model(&model.XhsScriptNote{}).
		Where("id = ?", existing.ID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("update after duplicate: %w", err)
	}
	return tx.WithContext(ctx).First(note, existing.ID).Error
}

func (s *xhsScriptStore) GetNote(ctx context.Context, userID uint, id uint64) (*model.XhsScriptNote, error) {
	var note model.XhsScriptNote
	if err := s.db.WithContext(ctx).Where("user_id = ? AND id = ?", userID, id).First(&note).Error; err != nil {
		return nil, fmt.Errorf("GetNote: %w", err)
	}
	return &note, nil
}

func (s *xhsScriptStore) ListNotes(ctx context.Context, userID uint, limit, offset int) ([]model.XhsScriptNote, error) {
	var notes []model.XhsScriptNote
	query := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&notes).Error; err != nil {
		return nil, fmt.Errorf("ListNotes: %w", err)
	}
	return notes, nil
}

func (s *xhsScriptStore) UpdateNoteVideoURL(ctx context.Context, userID uint, id uint64, videoURL string) error {
	if err := s.db.WithContext(ctx).Model(&model.XhsScriptNote{}).
		Where("user_id = ? AND id = ?", userID, id).
		Updates(map[string]interface{}{
			"video_url":  videoURL,
			"updated_at": time.Now(),
		}).Error; err != nil {
		return fmt.Errorf("UpdateNoteVideoURL: %w", err)
	}
	return nil
}

func (s *xhsScriptStore) UpdateTranscribeStatus(ctx context.Context, userID uint, id uint64, status string, transcript *string, lastError string) error {
	if err := s.db.WithContext(ctx).Model(&model.XhsScriptNote{}).
		Where("user_id = ? AND id = ?", userID, id).
		Updates(map[string]interface{}{
			"transcribe_status": status,
			"video_transcript":  transcript,
			"last_error":        lastError,
			"updated_at":        time.Now(),
		}).Error; err != nil {
		return fmt.Errorf("UpdateTranscribeStatus: %w", err)
	}
	return nil
}

func (s *xhsScriptStore) UpdateGenerateStatus(ctx context.Context, userID uint, id uint64, status string, lastError string) error {
	if err := s.db.WithContext(ctx).Model(&model.XhsScriptNote{}).
		Where("user_id = ? AND id = ?", userID, id).
		Updates(map[string]interface{}{
			"generate_status": status,
			"last_error":      lastError,
			"updated_at":      time.Now(),
		}).Error; err != nil {
		return fmt.Errorf("UpdateGenerateStatus: %w", err)
	}
	return nil
}

func (s *xhsScriptStore) CreateGeneration(ctx context.Context, userID uint, noteID uint64, scriptText string, promptTokens, completionTokens int) (*model.XhsScriptGeneration, error) {
	var generation model.XhsScriptGeneration
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var note model.XhsScriptNote
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND id = ?", userID, noteID).
			First(&note).Error; err != nil {
			return fmt.Errorf("lock note: %w", err)
		}

		var latestVersion int
		if err := tx.Model(&model.XhsScriptGeneration{}).
			Where("note_id = ?", noteID).
			Select("COALESCE(MAX(version), 0)").
			Scan(&latestVersion).Error; err != nil {
			return fmt.Errorf("latest version: %w", err)
		}
		generation = model.XhsScriptGeneration{
			UserID:           userID,
			NoteID:           noteID,
			Version:          latestVersion + 1,
			ScriptText:       scriptText,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
		}
		if err := tx.Create(&generation).Error; err != nil {
			return fmt.Errorf("create generation: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("CreateGeneration: %w", err)
	}
	return &generation, nil
}

func (s *xhsScriptStore) CreateGenerationAndDeductQuota(ctx context.Context, userID uint, noteID uint64, scriptText string, promptTokens, completionTokens int) (*XhsScriptGenerationCommit, error) {
	commit := &XhsScriptGenerationCommit{}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var note model.XhsScriptNote
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ? AND id = ?", userID, noteID).
			First(&note).Error; err != nil {
			return fmt.Errorf("lock note: %w", err)
		}

		account, err := findOrCreateQuotaAccountForUpdate(ctx, tx, userID)
		if err != nil {
			return fmt.Errorf("quota account: %w", err)
		}
		commit.FreeBefore = account.FreeRemaining
		commit.PaidBefore = account.PaidRemaining

		switch {
		case account.FreeRemaining > 0:
			account.FreeRemaining--
			commit.Bucket = model.XhsScriptQuotaBucketFree
		case account.PaidRemaining > 0:
			account.PaidRemaining--
			commit.Bucket = model.XhsScriptQuotaBucketPaid
		default:
			return ErrXhsScriptQuotaInsufficient
		}

		var latestVersion int
		if err := tx.Model(&model.XhsScriptGeneration{}).
			Where("note_id = ?", noteID).
			Select("COALESCE(MAX(version), 0)").
			Scan(&latestVersion).Error; err != nil {
			return fmt.Errorf("latest version: %w", err)
		}
		generation := &model.XhsScriptGeneration{
			UserID:           userID,
			NoteID:           noteID,
			Version:          latestVersion + 1,
			ScriptText:       scriptText,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
		}
		if err := tx.Create(generation).Error; err != nil {
			return fmt.Errorf("create generation: %w", err)
		}

		ledger := model.XhsScriptQuotaLedger{
			UserID:  userID,
			Delta:   -1,
			Bucket:  commit.Bucket,
			Reason:  model.XhsScriptLedgerReasonGeneration,
			RefType: model.XhsScriptLedgerRefTypeGeneration,
			RefID:   fmt.Sprintf("%d", generation.ID),
		}
		inserted, err := insertQuotaLedgerOnce(ctx, tx, &ledger)
		if err != nil {
			return fmt.Errorf("create ledger: %w", err)
		}
		if !inserted {
			return fmt.Errorf("generation quota ledger already exists for generation %d", generation.ID)
		}

		if err := tx.Model(&model.XhsScriptQuotaAccount{}).
			Where("id = ?", account.ID).
			Updates(map[string]interface{}{
				"free_remaining": account.FreeRemaining,
				"paid_remaining": account.PaidRemaining,
				"updated_at":     time.Now(),
			}).Error; err != nil {
			return fmt.Errorf("update quota account: %w", err)
		}

		if err := tx.Model(&model.XhsScriptNote{}).
			Where("id = ?", note.ID).
			Updates(map[string]interface{}{
				"generate_status": model.XhsScriptGenerateGenerated,
				"last_error":      "",
				"updated_at":      time.Now(),
			}).Error; err != nil {
			return fmt.Errorf("mark generated: %w", err)
		}

		commit.Generation = generation
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrXhsScriptQuotaInsufficient) {
			return nil, ErrXhsScriptQuotaInsufficient
		}
		return nil, fmt.Errorf("CreateGenerationAndDeductQuota: %w", err)
	}
	return commit, nil
}

func (s *xhsScriptStore) DeductOneGeneration(ctx context.Context, userID uint, refID string) error {
	if refID == "" {
		return ErrXhsScriptQuotaRefRequired
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		account, err := findOrCreateQuotaAccountForUpdate(ctx, tx, userID)
		if err != nil {
			return fmt.Errorf("quota account: %w", err)
		}

		applied, err := quotaLedgerExistsForUpdate(ctx, tx, userID, model.XhsScriptLedgerReasonGeneration, model.XhsScriptLedgerRefTypeGeneration, refID)
		if err != nil {
			return fmt.Errorf("check ledger: %w", err)
		}
		if applied {
			return nil
		}

		bucket := ""
		switch {
		case account.FreeRemaining > 0:
			account.FreeRemaining--
			bucket = model.XhsScriptQuotaBucketFree
		case account.PaidRemaining > 0:
			account.PaidRemaining--
			bucket = model.XhsScriptQuotaBucketPaid
		default:
			return ErrXhsScriptQuotaInsufficient
		}

		ledger := model.XhsScriptQuotaLedger{
			UserID:  userID,
			Delta:   -1,
			Bucket:  bucket,
			Reason:  model.XhsScriptLedgerReasonGeneration,
			RefType: model.XhsScriptLedgerRefTypeGeneration,
			RefID:   refID,
		}
		inserted, err := insertQuotaLedgerOnce(ctx, tx, &ledger)
		if err != nil {
			return fmt.Errorf("create ledger: %w", err)
		}
		if !inserted {
			return nil
		}

		if err := tx.Model(&model.XhsScriptQuotaAccount{}).
			Where("id = ?", account.ID).
			Updates(map[string]interface{}{
				"free_remaining": account.FreeRemaining,
				"paid_remaining": account.PaidRemaining,
				"updated_at":     time.Now(),
			}).Error; err != nil {
			return fmt.Errorf("update quota account: %w", err)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrXhsScriptQuotaInsufficient) {
			return ErrXhsScriptQuotaInsufficient
		}
		return fmt.Errorf("DeductOneGeneration: %w", err)
	}
	return nil
}

func findOrCreateQuotaAccountForUpdate(ctx context.Context, tx *gorm.DB, userID uint) (*model.XhsScriptQuotaAccount, error) {
	var account model.XhsScriptQuotaAccount
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", userID).
		First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		account = model.XhsScriptQuotaAccount{UserID: userID, FreeRemaining: 3}
		if err := tx.WithContext(ctx).Create(&account).Error; err != nil {
			if isDuplicateKeyErr(err) {
				if reloadErr := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
					Where("user_id = ?", userID).
					First(&account).Error; reloadErr != nil {
					return nil, fmt.Errorf("reload default account after duplicate: %w", reloadErr)
				}
				return &account, nil
			}
			return nil, fmt.Errorf("create default account: %w", err)
		}
		return &account, nil
	}
	if err != nil {
		return nil, err
	}
	return &account, nil
}

func insertQuotaLedgerOnce(ctx context.Context, tx *gorm.DB, ledger *model.XhsScriptQuotaLedger) (bool, error) {
	res := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "user_id"},
			{Name: "reason"},
			{Name: "ref_type"},
			{Name: "ref_id"},
		},
		DoNothing: true,
	}).Create(ledger)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

func quotaLedgerExistsForUpdate(ctx context.Context, tx *gorm.DB, userID uint, reason, refType, refID string) (bool, error) {
	var ledger model.XhsScriptQuotaLedger
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND reason = ? AND ref_type = ? AND ref_id = ?", userID, reason, refType, refID).
		First(&ledger).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func applyCapturedNoteDefaults(note *model.XhsScriptNote) {
	if note.NoteType == "" {
		note.NoteType = model.XhsScriptNoteTypeNormal
	}
	if note.TranscribeStatus == "" {
		note.TranscribeStatus = model.XhsScriptTranscribePending
	}
	if note.GenerateStatus == "" {
		note.GenerateStatus = model.XhsScriptGenerateNotReady
	}
}

func capturedNoteMetadataUpdateMap(note *model.XhsScriptNote) map[string]interface{} {
	return map[string]interface{}{
		"note_url":      note.NoteURL,
		"note_type":     note.NoteType,
		"title":         note.Title,
		"description":   note.Description,
		"tags":          note.Tags,
		"like_count":    note.LikeCount,
		"collect_count": note.CollectCount,
		"comment_count": note.CommentCount,
		"hot_comments":  note.HotComments,
		"cover_url":     note.CoverURL,
		"video_url":     note.VideoURL,
		"updated_at":    time.Now(),
	}
}

func preserveMirroredVideoURLOnRecapture(updates map[string]interface{}, existingVideoURL string) {
	if isXhsScriptMirroredVideoURL(existingVideoURL) {
		delete(updates, "video_url")
	}
}

func isXhsScriptMirroredVideoURL(videoURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(videoURL))
	if err != nil {
		return false
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = parsed.Path
	}
	if decoded, decErr := url.PathUnescape(path); decErr == nil {
		path = decoded
	}
	return strings.Contains(path, "/xhs-script-media/")
}
