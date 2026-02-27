package session

import "time"

const (
	DefaultDailyResetHour   = 4
	DefaultDailyResetMinute = 0
)

type DailyResetPolicy struct {
	Enabled     bool
	ResetHour   int
	ResetMinute int
	Location    *time.Location
}

func DefaultDailyResetPolicy() DailyResetPolicy {
	return DailyResetPolicy{
		Enabled:     true,
		ResetHour:   DefaultDailyResetHour,
		ResetMinute: DefaultDailyResetMinute,
		Location:    time.Local,
	}
}

func IsStaleByDailyReset(lastActivity, now time.Time, policy DailyResetPolicy) bool {
	normalized := normalizeDailyResetPolicy(policy)
	if !normalized.Enabled || lastActivity.IsZero() || now.IsZero() {
		return false
	}

	loc := normalized.Location
	if loc == nil {
		loc = time.Local
	}
	lastLocal := lastActivity.In(loc)
	nowLocal := now.In(loc)
	if nowLocal.Before(lastLocal) {
		return false
	}

	resetBoundary := dailyResetBoundary(nowLocal, normalized.ResetHour, normalized.ResetMinute)
	return lastLocal.Before(resetBoundary)
}

func normalizeDailyResetPolicy(policy DailyResetPolicy) DailyResetPolicy {
	if policy.ResetHour < 0 || policy.ResetHour > 23 {
		policy.ResetHour = DefaultDailyResetHour
	}
	if policy.ResetMinute < 0 || policy.ResetMinute > 59 {
		policy.ResetMinute = DefaultDailyResetMinute
	}
	if policy.Location == nil {
		policy.Location = time.Local
	}
	return policy
}

func dailyResetBoundary(nowLocal time.Time, resetHour, resetMinute int) time.Time {
	boundary := time.Date(
		nowLocal.Year(),
		nowLocal.Month(),
		nowLocal.Day(),
		resetHour,
		resetMinute,
		0,
		0,
		nowLocal.Location(),
	)
	if nowLocal.Before(boundary) {
		boundary = boundary.Add(-24 * time.Hour)
	}
	return boundary
}
