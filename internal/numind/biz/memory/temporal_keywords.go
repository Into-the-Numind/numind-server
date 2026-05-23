package memory

import (
	"regexp"
	"sort"
	"time"
)

// Granularity is one of the 4 digest periods supported by Task 3.8 temporal tree.
type Granularity string

const (
	GranDaily     Granularity = "daily"
	GranWeekly    Granularity = "weekly"
	GranMonthly   Granularity = "monthly"
	GranQuarterly Granularity = "quarterly"
)

// granRank gives the priority order (lower = finer / preferred). Used when
// MatchTimeKeywords needs to truncate to the spec-mandated ≤ 2 matches —
// finer granularity wins (daily > weekly > monthly > quarterly).
var granRank = map[Granularity]int{
	GranDaily:     0,
	GranWeekly:    1,
	GranMonthly:   2,
	GranQuarterly: 3,
}

// ShanghaiTZ is the canonical timezone for all temporal boundary math
// (spec §设计要点 时区). Loaded once at init; callers should pass it into
// time.Now().In(...) when computing "today" / "yesterday" / "本周" etc.
const ShanghaiTZ = "Asia/Shanghai"

// shanghaiLoc is the resolved Asia/Shanghai location. Loaded once at package
// init; falls back to time.UTC if the platform's zoneinfo is incomplete (rare
// — every supported deploy target has Asia/Shanghai).
var shanghaiLoc *time.Location

func init() {
	loc, err := time.LoadLocation(ShanghaiTZ)
	if err != nil {
		// Defensive fallback — UTC date math will mis-bucket boundaries by 8h
		// but won't crash. Production deployments have Asia/Shanghai zoneinfo.
		loc = time.UTC
	}
	shanghaiLoc = loc
}

// ShanghaiLocation returns the cached Asia/Shanghai *time.Location.
// Public for callers (cron jobs, temporal service) that need the same TZ.
func ShanghaiLocation() *time.Location { return shanghaiLoc }

// timeKWOffsetAbsoluteQuarter is the sentinel Offset value for Q1-Q4 absolute
// matches. The lookup logic walks backwards through the past 4 quarters to find
// the most recent quarter matching the requested label.
const timeKWOffsetAbsoluteQuarter = -99

// timeKW is one keyword-pattern entry.
//
//	Pattern: compiled regex applied to the full user-input string
//	Gran:    the digest granularity this match wants
//	Offset:  relative period offset (0 = current, -1 = previous, etc.);
//	         -99 = absolute Q1-Q4 lookup (walk back ≤ 4 quarters)
//	Label:   human-readable label injected into the system prompt block
//	         (e.g. "昨日", "上周", "Q3")
type timeKW struct {
	Pattern *regexp.Regexp
	Gran    Granularity
	Offset  int
	Label   string
}

// timeKWs is the canonical keyword catalogue (spec §设计要点). Ordering inside
// each granularity does not matter (MatchTimeKeywords dedupes by gran+offset);
// across granularities, finer granularity is preferred via granRank tie-break.
//
// CN+EN coverage:
//   - daily:     今天/今日/today, 昨天/昨日/yesterday, 前天/day before yesterday
//   - weekly:    本周/这周/本礼拜/this week, 上周/上礼拜/last week/过去一周
//   - monthly:   本月/这个月/当月/this month, 上月/上个月/last month
//   - quarterly: 本季度/这季度/当季/this quarter, 上季度/上一季/last quarter,
//     Q1-Q4/第一-第四季度 (absolute, offset=-99)
//
// Note on Chinese keywords: '当月' / '当季' are intentionally singular tokens
// (no whitespace splitting needed). Regex uses `|` alternation — the
// longest-match heuristic is not needed since each keyword is short and
// non-overlapping.
// Note: English alternatives use the `(?i)` flag so "Today" / "TODAY" / "Q1" /
// "q1" all match. Chinese characters are case-insensitive by definition.
var timeKWs = []timeKW{
	// daily
	{regexp.MustCompile(`(?i)今天|今日|today`), GranDaily, 0, "今日"},
	{regexp.MustCompile(`(?i)昨天|昨日|yesterday`), GranDaily, -1, "昨日"},
	{regexp.MustCompile(`(?i)前天|day before yesterday`), GranDaily, -2, "前日"},
	// weekly
	{regexp.MustCompile(`(?i)本周|这周|this week|本礼拜`), GranWeekly, 0, "本周"},
	{regexp.MustCompile(`(?i)上周|上礼拜|last week|过去一周`), GranWeekly, -1, "上周"},
	// monthly
	{regexp.MustCompile(`(?i)本月|这个月|当月|this month`), GranMonthly, 0, "本月"},
	{regexp.MustCompile(`(?i)上月|上个月|last month`), GranMonthly, -1, "上月"},
	// quarterly
	{regexp.MustCompile(`(?i)本季度|这季度|当季|this quarter`), GranQuarterly, 0, "本季度"},
	{regexp.MustCompile(`(?i)上季度|上一季|last quarter`), GranQuarterly, -1, "上季度"},
	// quarterly absolute (offset=-99 triggers "past 4 quarters, most recent match")
	{regexp.MustCompile(`(?i)Q1|第一季度`), GranQuarterly, timeKWOffsetAbsoluteQuarter, "Q1"},
	{regexp.MustCompile(`(?i)Q2|第二季度`), GranQuarterly, timeKWOffsetAbsoluteQuarter, "Q2"},
	{regexp.MustCompile(`(?i)Q3|第三季度`), GranQuarterly, timeKWOffsetAbsoluteQuarter, "Q3"},
	{regexp.MustCompile(`(?i)Q4|第四季度`), GranQuarterly, timeKWOffsetAbsoluteQuarter, "Q4"},
}

// TimeKeywordMatch is a resolved match — keyword pattern hit, but the actual
// time period still has to be resolved at fetch time (now-relative).
type TimeKeywordMatch struct {
	Gran   Granularity
	Offset int    // 0 / -1 / -2 / -99
	Label  string // "昨日" / "上周" / "Q3"
}

// MaxKeywordMatches is the spec-mandated cap on injected digest blocks
// (avoids prompt token bloat).
const MaxKeywordMatches = 2

// MatchTimeKeywords scans userInput for time keywords and returns up to
// MaxKeywordMatches matches, sorted by granularity priority (daily first).
//
// Dedup rules:
//   - identical (Gran, Offset) pairs collapsed to one (catch "今天和今日")
//   - "Q3" + "第三季度" (same label) → kept as one (regex shares Label)
//   - across distinct keywords matching the same time period → first match wins
//
// Sort: finer granularity first (granRank ascending). On tie, prefer
// negative offset (last-period) over 0 (current-period) — historical lookups
// are the more common ask in practice.
func MatchTimeKeywords(userInput string) []TimeKeywordMatch {
	if userInput == "" {
		return nil
	}
	type key struct {
		Gran  Granularity
		Off   int
		Label string
	}
	seen := make(map[key]struct{}, 4)
	var hits []TimeKeywordMatch
	for _, kw := range timeKWs {
		if !kw.Pattern.MatchString(userInput) {
			continue
		}
		k := key{Gran: kw.Gran, Off: kw.Offset, Label: kw.Label}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		hits = append(hits, TimeKeywordMatch{
			Gran:   kw.Gran,
			Offset: kw.Offset,
			Label:  kw.Label,
		})
	}
	if len(hits) <= 1 {
		return hits
	}
	// Sort: finer granularity first; on tie, prefer earlier offset (more negative).
	sort.SliceStable(hits, func(i, j int) bool {
		ri, rj := granRank[hits[i].Gran], granRank[hits[j].Gran]
		if ri != rj {
			return ri < rj
		}
		return hits[i].Offset < hits[j].Offset
	})
	if len(hits) > MaxKeywordMatches {
		hits = hits[:MaxKeywordMatches]
	}
	return hits
}

// ─── Period math helpers ─────────────────────────────────────────────────────

// PeriodBounds resolves a keyword match against the given "now" into the
// concrete digest-period identifier. Returned values vary by Gran:
//
//	GranDaily:     date is the target day (Asia/Shanghai); year/month/etc. unused
//	GranWeekly:    isoYear+isoWeek populated; date is weekStartDate (Mon)
//	GranMonthly:   year+month populated; date is the 1st of that month
//	GranQuarterly: year+quarter populated; date is the 1st of the quarter's 1st month
//
// All math is performed in Asia/Shanghai timezone (now is normalised via
// .In(ShanghaiLocation()) at entry).
//
// Returns ok=false when:
//   - GranQuarterly + Offset=-99 + Label cannot be parsed as Q1..Q4
//   - any internal date arithmetic produces an out-of-range result (no longer
//     possible after the existing guards — kept defensively)
type ResolvedPeriod struct {
	Gran     Granularity
	Date     time.Time // representative date (daily=target, weekly=Mon, monthly/quarterly=period start)
	ISOYear  int       // weekly only
	ISOWeek  int       // weekly only
	Year     int       // monthly + quarterly
	Month    int       // monthly only (1-12)
	Quarter  int       // quarterly only (1-4)
	WeekFrom time.Time // weekly only (Mon 00:00 Asia/Shanghai)
	WeekTo   time.Time // weekly only (Sun 23:59:59.999 Asia/Shanghai)
}

// ResolvePeriod converts a TimeKeywordMatch + now into a concrete ResolvedPeriod
// suitable for store fetch. now is interpreted in its own location (callers
// pass time.Now() or a fixed test clock); internally normalised to Asia/Shanghai.
func ResolvePeriod(m TimeKeywordMatch, now time.Time) (ResolvedPeriod, bool) {
	now = now.In(shanghaiLoc)
	switch m.Gran {
	case GranDaily:
		// Snap to midnight, then offset days.
		base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, shanghaiLoc)
		target := base.AddDate(0, 0, m.Offset)
		return ResolvedPeriod{Gran: GranDaily, Date: target}, true
	case GranWeekly:
		// Snap to Monday of current ISO week, then offset weeks.
		mon := mondayOfWeek(now)
		target := mon.AddDate(0, 0, 7*m.Offset)
		isoYear, isoWeek := target.ISOWeek()
		weekTo := target.AddDate(0, 0, 6) // Sunday
		return ResolvedPeriod{
			Gran:     GranWeekly,
			Date:     target,
			ISOYear:  isoYear,
			ISOWeek:  isoWeek,
			WeekFrom: target,
			WeekTo:   time.Date(weekTo.Year(), weekTo.Month(), weekTo.Day(), 23, 59, 59, int(time.Second-time.Nanosecond), shanghaiLoc),
		}, true
	case GranMonthly:
		// Add Offset months to the 1st of current month (clamping handled by
		// time.Date's normalisation, e.g. Mar 1 + (-1mo) = Feb 1).
		baseY, baseM := now.Year(), int(now.Month())
		y, m := addMonths(baseY, baseM, m.Offset)
		first := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, shanghaiLoc)
		return ResolvedPeriod{Gran: GranMonthly, Date: first, Year: y, Month: m}, true
	case GranQuarterly:
		curYear, curQ := quarterOf(now)
		var targetY, targetQ int
		if m.Offset == timeKWOffsetAbsoluteQuarter {
			// Absolute Q1-Q4 lookup: walk back ≤ 4 quarters to find a match.
			wantQ, ok := parseAbsoluteQuarter(m.Label)
			if !ok {
				return ResolvedPeriod{}, false
			}
			// Search current quarter then walk back; per spec §边界 case "Q3 when current Q2 → 2025Q3".
			// Try the current period first; if wantQ != curQ, walk back through (year, q) pairs.
			ty, tq := curYear, curQ
			for i := 0; i < 4; i++ {
				if tq == wantQ {
					targetY, targetQ = ty, tq
					goto found
				}
				// Step back one quarter.
				tq--
				if tq < 1 {
					tq = 4
					ty--
				}
			}
			return ResolvedPeriod{}, false
		found:
		} else {
			// Relative offset (0=current, -1=last, etc.).
			targetY, targetQ = curYear, curQ+m.Offset
			for targetQ < 1 {
				targetQ += 4
				targetY--
			}
			for targetQ > 4 {
				targetQ -= 4
				targetY++
			}
		}
		// Representative date: 1st of the quarter's 1st month.
		firstMonth := time.Month((targetQ-1)*3 + 1)
		first := time.Date(targetY, firstMonth, 1, 0, 0, 0, 0, shanghaiLoc)
		return ResolvedPeriod{Gran: GranQuarterly, Date: first, Year: targetY, Quarter: targetQ}, true
	}
	return ResolvedPeriod{}, false
}

// mondayOfWeek returns the Monday 00:00:00 of the ISO week containing t.
// ISO weeks start on Monday. Go's time.Weekday: Sunday=0, Monday=1, ..., Saturday=6.
func mondayOfWeek(t time.Time) time.Time {
	loc := t.Location()
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7 // Sunday → 7 in ISO numbering
	}
	// Days to subtract to reach Monday.
	delta := wd - 1
	mon := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return mon.AddDate(0, 0, -delta)
}

// addMonths handles arbitrary month offsets, wrapping into adjacent years.
// Returns (year, month-1-indexed-1..12).
func addMonths(year, month, delta int) (int, int) {
	// Normalise to 0-indexed before arithmetic, then re-pivot to 1-indexed.
	zero := (month - 1) + delta
	y := year + (zero / 12)
	m := (zero % 12) + 1
	if m < 1 {
		m += 12
		y--
	}
	return y, m
}

// quarterOf returns the (year, quarter 1-4) of the given time.
func quarterOf(t time.Time) (int, int) {
	return t.Year(), (int(t.Month())-1)/3 + 1
}

// parseAbsoluteQuarter maps a Q1-Q4 label to the 1-4 numeric quarter.
func parseAbsoluteQuarter(label string) (int, bool) {
	switch label {
	case "Q1":
		return 1, true
	case "Q2":
		return 2, true
	case "Q3":
		return 3, true
	case "Q4":
		return 4, true
	}
	return 0, false
}
