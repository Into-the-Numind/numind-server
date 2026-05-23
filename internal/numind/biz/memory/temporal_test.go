package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Keyword match table ─────────────────────────────────────────────────────

func TestMatchTimeKeywords_Daily(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantGran  Granularity
		wantOff   int
		wantLabel string
	}{
		{"today_cn", "我今天做了什么", GranDaily, 0, "今日"},
		{"yesterday_cn", "昨天的会议怎么样", GranDaily, -1, "昨日"},
		{"yesterday_en", "what did I do yesterday?", GranDaily, -1, "昨日"},
		{"day_before_yesterday", "前天上午开的会", GranDaily, -2, "前日"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			hits := MatchTimeKeywords(c.input)
			if assert.Len(t, hits, 1, "expected exactly 1 hit for %q", c.input) {
				assert.Equal(t, c.wantGran, hits[0].Gran)
				assert.Equal(t, c.wantOff, hits[0].Offset)
				assert.Equal(t, c.wantLabel, hits[0].Label)
			}
		})
	}
}

func TestMatchTimeKeywords_Weekly(t *testing.T) {
	hits := MatchTimeKeywords("上周战况如何")
	if assert.Len(t, hits, 1) {
		assert.Equal(t, GranWeekly, hits[0].Gran)
		assert.Equal(t, -1, hits[0].Offset)
		assert.Equal(t, "上周", hits[0].Label)
	}
}

func TestMatchTimeKeywords_Monthly(t *testing.T) {
	hits := MatchTimeKeywords("本月跟进的客户")
	if assert.Len(t, hits, 1) {
		assert.Equal(t, GranMonthly, hits[0].Gran)
		assert.Equal(t, 0, hits[0].Offset)
	}
}

func TestMatchTimeKeywords_Quarterly(t *testing.T) {
	hits := MatchTimeKeywords("Q3 业绩怎么样")
	if assert.Len(t, hits, 1) {
		assert.Equal(t, GranQuarterly, hits[0].Gran)
		assert.Equal(t, timeKWOffsetAbsoluteQuarter, hits[0].Offset)
		assert.Equal(t, "Q3", hits[0].Label)
	}
}

func TestMatchTimeKeywords_MultiHit(t *testing.T) {
	// 4 distinct keywords in one input — spec caps at 2, finer wins.
	hits := MatchTimeKeywords("昨天 上周 本月 Q3 综合看一下")
	assert.Len(t, hits, 2, "expected exactly MaxKeywordMatches=2 hits")
	// Daily wins over weekly/monthly/quarterly.
	assert.Equal(t, GranDaily, hits[0].Gran, "first hit should be daily (finest)")
	assert.Equal(t, GranWeekly, hits[1].Gran, "second hit should be weekly")
}

func TestMatchTimeKeywords_NoTime(t *testing.T) {
	hits := MatchTimeKeywords("帮我写一个客户开场白")
	assert.Empty(t, hits, "no time keywords expected")
}

func TestMatchTimeKeywords_Empty(t *testing.T) {
	assert.Empty(t, MatchTimeKeywords(""))
}

func TestMatchTimeKeywords_DedupSameGranOffset(t *testing.T) {
	// "今天" + "today" both match (daily/0), but should dedupe to 1.
	hits := MatchTimeKeywords("today 今天怎么样")
	if assert.Len(t, hits, 1) {
		assert.Equal(t, GranDaily, hits[0].Gran)
		assert.Equal(t, 0, hits[0].Offset)
	}
}

// TestMatchTimeKeywords_EnglishCaseInsensitive verifies "Today" / "TODAY" /
// "q1" / "Q1" all match (regex uses (?i)). Chinese characters are case-
// insensitive by definition; this guard is specifically for the English
// alternatives — without (?i) "Today" would silently miss.
func TestMatchTimeKeywords_EnglishCaseInsensitive(t *testing.T) {
	cases := []struct {
		input    string
		wantGran Granularity
		wantOff  int
	}{
		{"What about Today's plan?", GranDaily, 0},
		{"TODAY recap please", GranDaily, 0},
		{"Yesterday's call notes", GranDaily, -1},
		{"YESTERDAY summary", GranDaily, -1},
		{"This Week status", GranWeekly, 0},
		{"LAST WEEK customers", GranWeekly, -1},
		{"q1 numbers", GranQuarterly, timeKWOffsetAbsoluteQuarter},
		{"Q1 numbers", GranQuarterly, timeKWOffsetAbsoluteQuarter},
		{"q3 vs Q3", GranQuarterly, timeKWOffsetAbsoluteQuarter}, // dedup → 1 hit
	}
	for _, c := range cases {
		c := c
		t.Run(c.input, func(t *testing.T) {
			hits := MatchTimeKeywords(c.input)
			require.NotEmpty(t, hits, "expected ≥1 hit for %q (case-insensitive match)", c.input)
			assert.Equal(t, c.wantGran, hits[0].Gran)
			assert.Equal(t, c.wantOff, hits[0].Offset)
		})
	}
}

// ─── ResolvePeriod ─────────────────────────────────────────────────────────────

// fixedNow returns a time.Time pinned to a known Asia/Shanghai instant for
// deterministic period math. 2026-05-23 (Saturday) was the date Task 3.8 spec
// was written; using it as the test anchor avoids weekday/quarter ambiguity.
//
// Note: ISO week for 2026-05-23 is 2026-W21 (Mon = 2026-05-18).
// Q for May 2026 is Q2.
func fixedNow() time.Time {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	return time.Date(2026, 5, 23, 14, 30, 0, 0, loc)
}

func TestResolvePeriod_DailyYesterday(t *testing.T) {
	m := TimeKeywordMatch{Gran: GranDaily, Offset: -1, Label: "昨日"}
	rp, ok := ResolvePeriod(m, fixedNow())
	if assert.True(t, ok) {
		assert.Equal(t, GranDaily, rp.Gran)
		assert.Equal(t, "2026-05-22", rp.Date.Format("2006-01-02"))
	}
}

func TestResolvePeriod_WeeklyLastWeek(t *testing.T) {
	m := TimeKeywordMatch{Gran: GranWeekly, Offset: -1, Label: "上周"}
	rp, ok := ResolvePeriod(m, fixedNow())
	if assert.True(t, ok) {
		assert.Equal(t, GranWeekly, rp.Gran)
		assert.Equal(t, 2026, rp.ISOYear)
		assert.Equal(t, 20, rp.ISOWeek) // last week of 2026-W21 (anchor) is W20
		assert.Equal(t, "2026-05-11", rp.WeekFrom.Format("2006-01-02"))
		assert.Equal(t, "2026-05-17", rp.WeekTo.Format("2006-01-02"))
	}
}

func TestResolvePeriod_MonthlyLastMonth(t *testing.T) {
	m := TimeKeywordMatch{Gran: GranMonthly, Offset: -1, Label: "上月"}
	rp, ok := ResolvePeriod(m, fixedNow())
	if assert.True(t, ok) {
		assert.Equal(t, GranMonthly, rp.Gran)
		assert.Equal(t, 2026, rp.Year)
		assert.Equal(t, 4, rp.Month)
	}
}

func TestResolvePeriod_QuarterlyLastQuarter(t *testing.T) {
	m := TimeKeywordMatch{Gran: GranQuarterly, Offset: -1, Label: "上季度"}
	rp, ok := ResolvePeriod(m, fixedNow())
	if assert.True(t, ok) {
		assert.Equal(t, 2026, rp.Year)
		assert.Equal(t, 1, rp.Quarter) // last quarter from Q2 = Q1
	}
}

func TestResolvePeriod_AbsoluteQuarter_Q3_FromQ2(t *testing.T) {
	// Anchor is 2026-Q2; user asks "Q3" → spec §边界 says take prior-year Q3
	// (the most recent Q3 walking back ≤ 4 quarters).
	m := TimeKeywordMatch{Gran: GranQuarterly, Offset: timeKWOffsetAbsoluteQuarter, Label: "Q3"}
	rp, ok := ResolvePeriod(m, fixedNow())
	if assert.True(t, ok) {
		assert.Equal(t, 2025, rp.Year, "Q3 from Q2 anchor walks back to 2025 Q3")
		assert.Equal(t, 3, rp.Quarter)
	}
}

func TestResolvePeriod_AbsoluteQuarter_Q2_FromQ2(t *testing.T) {
	// User asks "Q2" while anchor is Q2 — should resolve to current Q2.
	m := TimeKeywordMatch{Gran: GranQuarterly, Offset: timeKWOffsetAbsoluteQuarter, Label: "Q2"}
	rp, ok := ResolvePeriod(m, fixedNow())
	if assert.True(t, ok) {
		assert.Equal(t, 2026, rp.Year)
		assert.Equal(t, 2, rp.Quarter)
	}
}

func TestResolvePeriod_AbsoluteQuarter_InvalidLabel(t *testing.T) {
	m := TimeKeywordMatch{Gran: GranQuarterly, Offset: timeKWOffsetAbsoluteQuarter, Label: "Q9"}
	_, ok := ResolvePeriod(m, fixedNow())
	assert.False(t, ok, "Q9 is not a valid quarter")
}

// ─── ISO week boundary ─────────────────────────────────────────────────────────

func TestISOWeek_YearBoundary_2026_01_01(t *testing.T) {
	// 2026-01-01 is Thursday, which falls in ISO 2026-W01 (W1 is the week
	// containing the first Thursday of the year).
	loc, _ := time.LoadLocation("Asia/Shanghai")
	d := time.Date(2026, 1, 1, 12, 0, 0, 0, loc)
	year, week := d.ISOWeek()
	assert.Equal(t, 2026, year, "2026-01-01 should be ISO year 2026")
	assert.Equal(t, 1, week, "2026-01-01 should be ISO week 1")
}

func TestISOWeek_YearBoundary_2025_01_01(t *testing.T) {
	// 2025-01-01 is Wednesday — that week (Mon Dec 30 - Sun Jan 5) contains
	// only 3 days of 2025; the first Thursday is Jan 2, so 2025-W01 starts
	// Mon Dec 30 2024.
	loc, _ := time.LoadLocation("Asia/Shanghai")
	d := time.Date(2025, 1, 1, 12, 0, 0, 0, loc)
	year, week := d.ISOWeek()
	assert.Equal(t, 2025, year)
	assert.Equal(t, 1, week)
}

// Edge: 2024-12-31 is Tue, in the same ISO week (2025-W01) as Jan 1 2025.
func TestISOWeek_YearBoundary_2024_12_31(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	d := time.Date(2024, 12, 31, 12, 0, 0, 0, loc)
	year, week := d.ISOWeek()
	assert.Equal(t, 2025, year, "Dec 31 2024 belongs to ISO year 2025")
	assert.Equal(t, 1, week)
}

// ─── mondayOfWeek ─────────────────────────────────────────────────────────────

func TestMondayOfWeek_Saturday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	sat := time.Date(2026, 5, 23, 14, 30, 0, 0, loc)
	mon := mondayOfWeek(sat)
	assert.Equal(t, "2026-05-18", mon.Format("2006-01-02"))
}

func TestMondayOfWeek_Sunday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	sun := time.Date(2026, 5, 24, 14, 30, 0, 0, loc)
	mon := mondayOfWeek(sun)
	assert.Equal(t, "2026-05-18", mon.Format("2006-01-02"),
		"Sunday should map back to the Monday of the same ISO week")
}

func TestMondayOfWeek_Monday(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	mon := time.Date(2026, 5, 18, 14, 30, 0, 0, loc)
	got := mondayOfWeek(mon)
	assert.Equal(t, "2026-05-18", got.Format("2006-01-02"))
	assert.Equal(t, 0, got.Hour(), "should snap to midnight")
}
