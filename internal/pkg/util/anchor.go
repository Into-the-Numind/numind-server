package util

import "time"

// AnchorAddMonths 在 anchor 上加 n 个月，遵循 day 锚点规则：
// 目标 day = min(anchor.Day(), daysInMonth(targetYear, targetMonth))。
// 这避免 1/31 + 1 month 漂移到 3/3（time.AddDate 的标准漂移行为）。
//
// 不变量：
//   INV-1: AnchorAddMonths(a, 0) == a
//   INV-2: AnchorAddMonths(a, n).Day() <= a.Day()
//   INV-3: 时分秒纳秒与 anchor 完全一致
//
// n 必须 >= 0；n < 0 panic（编码 bug，调用方保证）。
func AnchorAddMonths(anchor time.Time, n int) time.Time {
	if n < 0 {
		panic("AnchorAddMonths: n must be >= 0")
	}
	if n == 0 {
		return anchor
	}

	y, m, d := anchor.Date()
	hh, mm, ss := anchor.Clock()
	nsec := anchor.Nanosecond()
	loc := anchor.Location()

	totalMonths := int(m) + n
	targetYear := y + (totalMonths-1)/12
	targetMonth := time.Month((totalMonths-1)%12 + 1)

	// time.Date(_, M+1, 0, ...) 自动归一化为 M 月最后一天
	lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, loc).Day()

	targetDay := d
	if targetDay > lastDay {
		targetDay = lastDay
	}

	return time.Date(targetYear, targetMonth, targetDay, hh, mm, ss, nsec, loc)
}
