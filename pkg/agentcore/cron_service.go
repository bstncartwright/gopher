package agentcore

import "context"

type CronJob struct {
	ID        string  `json:"id"`
	SessionID string  `json:"session_id"`
	Message   string  `json:"message"`
	CronExpr  string  `json:"cron_expr"`
	Timezone  string  `json:"timezone"`
	Enabled   bool    `json:"enabled"`
	CreatedBy string  `json:"created_by"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	LastRunAt *string `json:"last_run_at,omitempty"`
	NextRunAt *string `json:"next_run_at,omitempty"`
}

type CronCreateRequest struct {
	SessionID string
	Message   string
	CronExpr  string
	Timezone  string
	CreatedBy string
}

type CronListRequest struct {
	SessionID string
}

type CronToolService interface {
	CreateCronJob(ctx context.Context, req CronCreateRequest) (CronJob, error)
	ListCronJobs(ctx context.Context, req CronListRequest) ([]CronJob, error)
	DeleteCronJob(ctx context.Context, jobID string) (bool, error)
	PauseCronJob(ctx context.Context, jobID string) (CronJob, error)
	ResumeCronJob(ctx context.Context, jobID string) (CronJob, error)
}
