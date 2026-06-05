package sop

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
)

// fakeRunStatusStore is a minimal store.ISopStore stub whose UpdateRun fails the
// first failTimes calls, then succeeds. All other methods are nil (embedded
// interface) and panic if called — only UpdateRun is on the retry path.
type fakeRunStatusStore struct {
	store.ISopStore
	failTimes int
	calls     int
}

func (f *fakeRunStatusStore) UpdateRun(_ uint, _ map[string]interface{}) error {
	f.calls++
	if f.calls <= f.failTimes {
		return errors.New("transient db error")
	}
	return nil
}

// fakeRunStatusDatastore adapts the ISopStore stub onto store.IStore so a
// sopBiz can be constructed with just the Sop() dependency wired.
type fakeRunStatusDatastore struct {
	store.IStore
	sop store.ISopStore
}

func (f *fakeRunStatusDatastore) Sop() store.ISopStore { return f.sop }

func TestUpdateRunStatusWithRetry_SucceedsAfterTransientFailures(t *testing.T) {
	fs := &fakeRunStatusStore{failTimes: 2} // fail twice, succeed on the 3rd
	b := &sopBiz{ds: &fakeRunStatusDatastore{sop: fs}}

	err := b.updateRunStatusWithRetry(context.Background(), 1, map[string]interface{}{"status": "running"})
	require.NoError(t, err, "should succeed within retry budget")
	assert.Equal(t, 3, fs.calls, "should retry until success (2 fail + 1 ok)")
}

func TestUpdateRunStatusWithRetry_ExhaustsAndReturnsError(t *testing.T) {
	fs := &fakeRunStatusStore{failTimes: 99} // always fail
	b := &sopBiz{ds: &fakeRunStatusDatastore{sop: fs}}

	err := b.updateRunStatusWithRetry(context.Background(), 1, map[string]interface{}{"status": "running"})
	require.Error(t, err, "should return error after exhausting retries")
	assert.Equal(t, 3, fs.calls, "should attempt exactly 3 times then give up")
}
