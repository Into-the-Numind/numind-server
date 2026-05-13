package customer

import (
	"context"
	"testing"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"

	"github.com/stretchr/testify/assert"
)

// TestValidateSubUsersBelongToCaller 覆盖两步校验四个场景:
//   - 空名单: 直接 nil 通过
//   - 全部归属 caller: nil 通过
//   - 含不存在用户: ErrSubUserNotFound
//   - 含他人子用户: ErrCrossParentSubUser
//
// 详见 spec §4.1.8.
func TestValidateSubUsersBelongToCaller(t *testing.T) {
	db := newBizTestDB(t)
	s := store.NewTestStore(db)

	// seed: parent=1 / parent=2 (顶层); sub=10/11 属于 1; sub=20 属于 2
	parent1 := insertBizUser(t, db, nil)
	parent2 := insertBizUser(t, db, nil)
	sub10 := insertBizUser(t, db, &parent1)
	sub11 := insertBizUser(t, db, &parent1)
	sub20 := insertBizUser(t, db, &parent2)

	cases := []struct {
		name       string
		callerID   uint
		subUserIDs []uint
		wantErr    error
	}{
		{"empty list returns nil", parent1, nil, nil},
		{"happy path: all belong to caller", parent1, []uint{sub10, sub11}, nil},
		{"contains non-existent ID", parent1, []uint{sub10, 9999}, errno.ErrSubUserNotFound},
		{"contains other parent's sub-user", parent1, []uint{sub10, sub20}, errno.ErrCrossParentSubUser},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateSubUsersBelongToCaller(context.Background(), s, c.callerID, c.subUserIDs)
			if c.wantErr == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, c.wantErr)
			}
		})
	}
}
