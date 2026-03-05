package agentcore

import "context"

type CronJob struct {
	ID             string  `json:"id"`
	SessionID      string  `json:"session_id"`
	Title          string  `json:"title,omitempty"`
	Message        string  `json:"message"`
	CronExpr       string  `json:"cron_expr"`
	Timezone       string  `json:"timezone"`
	Mode           string  `json:"mode,omitempty"`
	NotifyActorID  string  `json:"notify_actor_id,omitempty"`
	TargetAgent    string  `json:"target_agent,omitempty"`
	ModelPolicy    string  `json:"model_policy,omitempty"`
	Enabled        bool    `json:"enabled"`
	CreatedBy      string  `json:"created_by"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	LastRunAt      *string `json:"last_run_at,omitempty"`
	NextRunAt      *string `json:"next_run_at,omitempty"`
	LastRunStatus  string  `json:"last_run_status,omitempty"`
	LastRunSummary string  `json:"last_run_summary,omitempty"`
	LastRunError   string  `json:"last_run_error,omitempty"`
}

type CronCreateRequest struct {
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
