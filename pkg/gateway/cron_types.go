package gateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	defaultCronTimezone = "UTC"
)

type CronJob struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	CronExpr  string `json:"cron_expr"`
	Timezone  string `json:"timezone"`
	Enabled   bool   `json:"enabled"`
	CreatedBy string `json:"created_by"`

	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
}

type CronCreateInput struct {
	SessionID string
	Message   string
	CronExpr  string
	Timezone  string
	CreatedBy string
}

func normalizeCronTimezone(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultCronTimezone
	}
	return value
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
