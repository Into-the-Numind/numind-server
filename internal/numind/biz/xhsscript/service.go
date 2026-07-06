package xhsscript

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/datatypes"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

const (
	SessionCookieName = "ys_xhs_script_token"
	ExtTokenScope     = "xhs-script"
	AnonymousPrefix   = "xhs_anon_"

	TrialFreeGenerations = int64(3)
	PackGenerations      = int64(10)
	PackAmountCents      = int64(1990)
)

type Service struct {
	ds            store.IStore
	transcribeSem chan struct{}
}

func New(ds store.IStore) *Service {
	return &Service{
		ds:            ds,
		transcribeSem: make(chan struct{}, 2),
	}
}

type SessionDTO struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	User        UserDTO   `json:"user"`
	Quota       QuotaDTO  `json:"quota"`
}

type UserDTO struct {
	ID          uint   `json:"id"`
	Username    string `json:"username"`
	Nickname    string `json:"nickname"`
	IsAnonymous bool   `json:"is_anonymous"`
}

type QuotaDTO struct {
	FreeRemaining int64 `json:"free_remaining"`
	PaidRemaining int64 `json:"paid_remaining"`
	Remaining     int64 `json:"remaining"`
	Total         int64 `json:"total"`
}

type ProfileDTO struct {
	ProfileText string `json:"profile_text"`
}

type HomeDTO struct {
	User    UserDTO    `json:"user"`
	Quota   QuotaDTO   `json:"quota"`
	Profile ProfileDTO `json:"profile"`
	Notes   []NoteDTO  `json:"notes"`
}

type NoteDTO struct {
	ID              uint64    `json:"id"`
	SourceNoteID    string    `json:"source_note_id"`
	NoteURL         string    `json:"note_url"`
	NoteType        string    `json:"note_type"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	Tags            []string  `json:"tags"`
	Author          string    `json:"author"`
	CoverURL        string    `json:"cover_url"`
	VideoURL        string    `json:"video_url"`
	LikeCount       int64     `json:"like_count"`
	CollectCount    int64     `json:"collect_count"`
	CommentCount    int64     `json:"comment_count"`
	HotComments     []Comment `json:"hot_comments"`
	VideoTranscript string    `json:"video_transcript"`
	ScriptText      string    `json:"script_text"`
	State           string    `json:"state"`
	LastError       string    `json:"last_error"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Comment struct {
	Content  string `json:"content"`
	Nickname string `json:"nickname,omitempty"`
	Like     int64  `json:"like,omitempty"`
}

func IsAnonymousUsername(username string) bool {
	return strings.HasPrefix(username, AnonymousPrefix)
}

func userDTO(user *model.User) UserDTO {
	if user == nil {
		return UserDTO{}
	}
	name := user.Nickname
	if strings.TrimSpace(name) == "" {
		name = user.Username
	}
	return UserDTO{
		ID:          user.ID,
		Username:    user.Username,
		Nickname:    name,
		IsAnonymous: IsAnonymousUsername(user.Username),
	}
}

func (s *Service) quotaDTO(ctx context.Context, account *model.XhsScriptQuotaAccount) (QuotaDTO, error) {
	if account == nil {
		return QuotaDTO{}, nil
	}
	remaining := account.FreeRemaining + account.PaidRemaining
	total := remaining
	var used int64
	err := s.ds.DB().WithContext(ctx).Model(&model.XhsScriptQuotaLedger{}).
		Where("user_id = ? AND reason = ? AND delta < 0", account.UserID, model.XhsScriptLedgerReasonGeneration).
		Count(&used).Error
	if err == nil {
		total += used
	}
	return QuotaDTO{
		FreeRemaining: account.FreeRemaining,
		PaidRemaining: account.PaidRemaining,
		Remaining:     remaining,
		Total:         total,
	}, nil
}

func tagsFromJSON(raw datatypes.JSON) []string {
	if len(raw) == 0 {
		return nil
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err == nil {
		return tags
	}
	return nil
}

func commentsFromJSON(raw datatypes.JSON) []Comment {
	if len(raw) == 0 {
		return nil
	}
	var comments []Comment
	if err := json.Unmarshal(raw, &comments); err == nil {
		return comments
	}
	return nil
}

func mustJSON(v interface{}) datatypes.JSON {
	b, err := json.Marshal(v)
	if err != nil {
		return datatypes.JSON([]byte("[]"))
	}
	return datatypes.JSON(b)
}

func noteState(note *model.XhsScriptNote) string {
	switch note.TranscribeStatus {
	case model.XhsScriptTranscribePending:
		return "waiting_transcript"
	case model.XhsScriptTranscribeTranscribing:
		return "transcribing"
	case model.XhsScriptTranscribeFailed, model.XhsScriptTranscribeEmpty:
		return "transcribe_failed"
	}
	switch note.GenerateStatus {
	case model.XhsScriptGenerateGenerating:
		return "generating"
	case model.XhsScriptGenerateGenerated:
		return "generated"
	case model.XhsScriptGenerateFailed:
		return "ready_to_generate"
	default:
		return "ready_to_generate"
	}
}

func latestGenerationScript(ctx context.Context, ds store.IStore, userID uint, noteID uint64) (string, error) {
	var generation model.XhsScriptGeneration
	err := ds.DB().WithContext(ctx).
		Where("user_id = ? AND note_id = ?", userID, noteID).
		Order("version DESC").
		First(&generation).Error
	if err != nil {
		return "", err
	}
	return generation.ScriptText, nil
}

func (s *Service) noteDTO(ctx context.Context, note *model.XhsScriptNote) (NoteDTO, error) {
	if note == nil {
		return NoteDTO{}, fmt.Errorf("nil xhs script note")
	}
	var transcript string
	if note.VideoTranscript != nil {
		transcript = *note.VideoTranscript
	}
	dto := NoteDTO{
		ID:              note.ID,
		SourceNoteID:    note.SourceNoteID,
		NoteURL:         note.NoteURL,
		NoteType:        note.NoteType,
		Title:           note.Title,
		Description:     note.Description,
		Tags:            tagsFromJSON(note.Tags),
		Author:          "小红书视频",
		CoverURL:        note.CoverURL,
		VideoURL:        resignXhsScriptVideoURL(ctx, note.VideoURL),
		LikeCount:       note.LikeCount,
		CollectCount:    note.CollectCount,
		CommentCount:    note.CommentCount,
		HotComments:     commentsFromJSON(note.HotComments),
		VideoTranscript: transcript,
		State:           noteState(note),
		LastError:       note.LastError,
		CreatedAt:       note.CreatedAt,
		UpdatedAt:       note.UpdatedAt,
	}
	if note.GenerateStatus == model.XhsScriptGenerateGenerated {
		if script, err := latestGenerationScript(ctx, s.ds, note.UserID, note.ID); err == nil {
			dto.ScriptText = script
		}
	}
	return dto, nil
}
