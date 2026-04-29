package util_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/util"
)

func mkUTC(y int, m time.Month, d, hh, mm, ss int) time.Time {
	return time.Date(y, m, d, hh, mm, ss, 0, time.UTC)
}

func TestAnchorAddMonths_Jan31Sequence(t *testing.T) {
	a := mkUTC(2026, time.January, 31, 10, 0, 0)
	require.Equal(t, mkUTC(2026, time.February, 28, 10, 0, 0), util.AnchorAddMonths(a, 1))
	require.Equal(t, mkUTC(2026, time.March, 31, 10, 0, 0), util.AnchorAddMonths(a, 2))
	require.Equal(t, mkUTC(2026, time.April, 30, 10, 0, 0), util.AnchorAddMonths(a, 3))
	require.Equal(t, mkUTC(2026, time.May, 31, 10, 0, 0), util.AnchorAddMonths(a, 4))
	require.Equal(t, mkUTC(2027, time.January, 31, 10, 0, 0), util.AnchorAddMonths(a, 12))
	require.Equal(t, mkUTC(2027, time.February, 28, 10, 0, 0), util.AnchorAddMonths(a, 13))
}

func TestAnchorAddMonths_LeapDay(t *testing.T) {
	a := mkUTC(2024, time.February, 29, 0, 0, 0)
	require.Equal(t, mkUTC(2024, time.March, 29, 0, 0, 0), util.AnchorAddMonths(a, 1))
	require.Equal(t, mkUTC(2025, time.February, 28, 0, 0, 0), util.AnchorAddMonths(a, 12))
	require.Equal(t, mkUTC(2026, time.February, 28, 0, 0, 0), util.AnchorAddMonths(a, 24))
	require.Equal(t, mkUTC(2028, time.February, 29, 0, 0, 0), util.AnchorAddMonths(a, 48))
}

func TestAnchorAddMonths_DayCapping(t *testing.T) {
	require.Equal(t, mkUTC(2026, time.June, 30, 12, 0, 0),
		util.AnchorAddMonths(mkUTC(2026, time.May, 31, 12, 0, 0), 1))
	require.Equal(t, mkUTC(2026, time.August, 31, 12, 0, 0),
		util.AnchorAddMonths(mkUTC(2026, time.July, 31, 12, 0, 0), 1))
}

func TestAnchorAddMonths_YearWrap(t *testing.T) {
	a := mkUTC(2026, time.December, 15, 23, 59, 59)
	require.Equal(t, mkUTC(2027, time.January, 15, 23, 59, 59), util.AnchorAddMonths(a, 1))
	require.Equal(t, mkUTC(2028, time.January, 15, 23, 59, 59), util.AnchorAddMonths(a, 13))
}

func TestAnchorAddMonths_ZeroIsIdentity(t *testing.T) {
	a := mkUTC(2026, time.March, 15, 8, 30, 0)
	require.Equal(t, a, util.AnchorAddMonths(a, 0))
}

func TestAnchorAddMonths_NegativePanics(t *testing.T) {
	require.Panics(t, func() {
		util.AnchorAddMonths(time.Now(), -1)
	})
}

func TestAnchorAddMonths_PreservesClockComponents(t *testing.T) {
	a := time.Date(2026, time.January, 31, 13, 45, 7, 123456789, time.UTC)
	got := util.AnchorAddMonths(a, 1)
	require.Equal(t, 13, got.Hour())
	require.Equal(t, 45, got.Minute())
	require.Equal(t, 7, got.Second())
	require.Equal(t, 123456789, got.Nanosecond())
}
