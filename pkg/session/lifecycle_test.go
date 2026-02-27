package session

import (
	"testing"
	"time"
)

func TestIsStaleByDailyReset(t *testing.T) {
	policy := DailyResetPolicy{
		Enabled:     true,
		ResetHour:   4,
		ResetMinute: 0,
		Location:    time.UTC,
	}
	now := time.Date(2026, time.February, 27, 10, 0, 0, 0, time.UTC)

	stale := time.Date(2026, time.February, 27, 3, 59, 59, 0, time.UTC)
	if !IsStaleByDailyReset(stale, now, policy) {
		t.Fatalf("expected last activity before reset boundary to be stale")
	}

	fresh := time.Date(2026, time.February, 27, 4, 0, 0, 0, time.UTC)
	if IsStaleByDailyReset(fresh, now, policy) {
		t.Fatalf("expected last activity at reset boundary to remain fresh")
	}
}

func TestIsStaleByDailyResetBeforeDailyBoundary(t *testing.T) {
	policy := DailyResetPolicy{
		Enabled:     true,
		ResetHour:   4,
		ResetMinute: 0,
		Location:    time.UTC,
	}
	now := time.Date(2026, time.February, 27, 3, 0, 0, 0, time.UTC)

	// Before 04:00 UTC, the active reset boundary is from the previous day.
	stale := time.Date(2026, time.February, 25, 23, 0, 0, 0, time.UTC)
	if !IsStaleByDailyReset(stale, now, policy) {
		t.Fatalf("expected session older than previous boundary to be stale")
	}

	fresh := time.Date(2026, time.February, 26, 4, 0, 0, 0, time.UTC)
	if IsStaleByDailyReset(fresh, now, policy) {
		t.Fatalf("expected session at previous boundary to remain fresh")
	}
}
