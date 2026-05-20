package memory

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// Notepad manages L2 (user_global_memory) key-value memory entries for a user.
// Each entry is scoped to a (userID, key) pair and may carry a MemoryKind, confidence,
// and source metadata.
type Notepad interface {
	// Write validates and upserts a single L2 memory entry.
	// kind must be valid for layer "L2" (summary is not allowed).
	// key must be ≤100 characters; value must be ≤1024 characters; userID must be >0.
	// The value is HTML-escaped via EscapeForStorage before being persisted.
	// WriteOpts.Confidence nil defaults to 1.0; &0.0 is stored as-is.
	Write(ctx context.Context, userID uint, kind MemoryKind, key, value string, opts WriteOpts) error

	// Read returns the MemoryItem for the given (userID, key).
	// Returns (nil, nil) when the entry does not exist.
	Read(ctx context.Context, userID uint, key string) (*MemoryItem, error)

	// ListByKind returns up to limit L2 entries for (userID, kind), ordered by updated_at desc.
	// limit ≤0 defaults to 50 (delegated to store).
	ListByKind(ctx context.Context, userID uint, kind MemoryKind, limit int) ([]MemoryItem, error)

	// Delete removes the entry for (userID, key). Silently succeeds if the entry does not exist.
	Delete(ctx context.Context, userID uint, key string) error
}

const (
	maxKeyLen   = 100
	maxValueLen = 1024
)

type notepadImpl struct {
	store store.IUserGlobalMemoryStore
}

// NewNotepad constructs a Notepad backed by the given IUserGlobalMemoryStore.
func NewNotepad(s store.IUserGlobalMemoryStore) Notepad {
	return &notepadImpl{store: s}
}

// Write validates inputs and upserts the L2 memory row.
func (n *notepadImpl) Write(ctx context.Context, userID uint, kind MemoryKind, key, value string, opts WriteOpts) error {
	// 1. kind must be valid for L2
	if !kind.Valid("L2") {
		return ErrMemoryKindInvalid
	}
	// 2. key length
	if len(key) > maxKeyLen {
		return ErrMemoryKeyTooLong
	}
	// 3. value length
	if len(value) > maxValueLen {
		return ErrMemoryValueTooLong
	}
	// 4. userID must be provided
	if userID == 0 {
		return ErrMemoryUserRequired
	}

	// Resolve confidence: nil → default 1.0
	confidence := 1.0
	if opts.Confidence != nil {
		confidence = *opts.Confidence
	}

	// Resolve source type
	sourceType := string(SourceAgentTool)
	if opts.SourceType != "" {
		sourceType = string(opts.SourceType)
	}

	m := &model.UserGlobalMemory{
		UserID:                  userID,
		Kind:                    string(kind),
		KeyName:                 key,
		Value:                   EscapeForStorage(value),
		Confidence:              confidence,
		SourceType:              sourceType,
		SourceAgentDefinitionID: opts.SourceAgentDefinitionID,
	}

	if err := n.store.Upsert(ctx, m); err != nil {
		return fmt.Errorf("notepad.Write: %w", err)
	}
	return nil
}

// Read retrieves the L2 entry for (userID, key). Returns (nil, nil) if not found.
func (n *notepadImpl) Read(ctx context.Context, userID uint, key string) (*MemoryItem, error) {
	row, err := n.store.GetByUserKey(ctx, userID, key)
	if err != nil {
		// errors.Is 穿透 %w 包装；store 即使 wrap 了 gorm.ErrRecordNotFound 也能命中（P1-1：删除重复 isNotFound 检测）
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("notepad.Read: %w", err)
	}
	item := rowToL2Item(row)
	return &item, nil
}

// ListByKind returns up to limit L2 entries for (userID, kind).
func (n *notepadImpl) ListByKind(ctx context.Context, userID uint, kind MemoryKind, limit int) ([]MemoryItem, error) {
	rows, err := n.store.ListByUserKind(ctx, userID, string(kind), limit)
	if err != nil {
		return nil, fmt.Errorf("notepad.ListByKind: %w", err)
	}
	items := make([]MemoryItem, len(rows))
	for i, r := range rows {
		items[i] = rowToL2Item(&r)
	}
	return items, nil
}

// Delete removes the entry for (userID, key).
func (n *notepadImpl) Delete(ctx context.Context, userID uint, key string) error {
	if err := n.store.DeleteByUserKey(ctx, userID, key); err != nil {
		return fmt.Errorf("notepad.Delete: %w", err)
	}
	return nil
}

// rowToL2Item maps a model.UserGlobalMemory row to a MemoryItem.
// Content is set to row.Value (the L2 unified field per types.go comment).
func rowToL2Item(row *model.UserGlobalMemory) MemoryItem {
	return MemoryItem{
		ID:                      row.ID,
		Kind:                    MemoryKind(row.Kind),
		Content:                 row.Value,
		SourceType:              SourceType(row.SourceType),
		SourceAgentDefinitionID: row.SourceAgentDefinitionID,
		CreatedAt:               row.CreatedAt,
		UpdatedAt:               row.UpdatedAt,
		// L2-specific
		KeyName:    row.KeyName,
		Confidence: row.Confidence,
	}
}
