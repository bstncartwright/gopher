package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	defaultCronTimezone = "UTC"
	CronModeSession     = "session"
	CronModeIsolated    = "isolated"

	CronRunStatusRunning   = "running"
	CronRunStatusCompleted = "completed"
	CronRunStatusFailed    = "failed"
)

type CronJob struct {
	ID            string `json:"id"`
	SessionID     string `json:"session_id"`
	Title         string `json:"title,omitempty"`
	Message       string `json:"message"`
	CronExpr      string `json:"cron_expr"`
	Timezone      string `json:"timezone"`
	Mode          string `json:"mode,omitempty"`
	NotifyActorID string `json:"notify_actor_id,omitempty"`
	TargetAgent   string `json:"target_agent,omitempty"`
	ModelPolicy   string `json:"model_policy,omitempty"`
	ActiveRunID   string `json:"active_run_id,omitempty"`
	Enabled       bool   `json:"enabled"`
	CreatedBy     string `json:"created_by"`

	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	NextRunAt      *time.Time `json:"next_run_at,omitempty"`
	LastRunStatus  string     `json:"last_run_status,omitempty"`
	LastRunSummary string     `json:"last_run_summary,omitempty"`
	LastRunError   string     `json:"last_run_error,omitempty"`
}

type CronCreateInput struct {
	SessionID     string
	Title         string
	Message       string
	CronExpr      string
	Timezone      string
	Mode          string
	NotifyActorID string
	TargetAgent   string
	ModelPolicy   string
	CreatedBy     string
}

func normalizeCronTimezone(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultCronTimezone
	}
	return value
}

func normalizeCronMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", CronModeSession:
		return CronModeSession
	case CronModeIsolated:
		return CronModeIsolated
	default:
		return ""
	}
}

func validCronRunStatus(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", CronRunStatusRunning, CronRunStatusCompleted, CronRunStatusFailed:
		return true
	default:
		return false
	}
}

func loadCronLocation(raw string) (*time.Location, error) {
	locationName := normalizeCronTimezone(raw)
	location, err := time.LoadLocation(locationName)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", locationName, err)
	}
	return location, nil
}

func parseCronSchedule(raw string) (cron.Schedule, error) {
	expr := strings.TrimSpace(raw)
	if expr == "" {
		return nil, fmt.Errorf("cron expression is required")
	}
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return schedule, nil
}

func nextCronRun(expr string, locationName string, from time.Time) (time.Time, error) {
	schedule, err := parseCronSchedule(expr)
	if err != nil {
		return time.Time{}, err
	}
	location, err := loadCronLocation(locationName)
	if err != nil {
		return time.Time{}, err
	}
	reference := from.In(location)
	next := schedule.Next(reference)
	if next.IsZero() {
		return time.Time{}, fmt.Errorf("cron expression %q produced no future run time", expr)
	}
	return next.UTC(), nil
}
