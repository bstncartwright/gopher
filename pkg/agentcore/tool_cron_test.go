package agentcore

import (
	"context"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type fakeCronToolService struct {
	lastCreate CronCreateRequest
	jobs       []CronJob
}

func (s *fakeCronToolService) CreateCronJob(_ context.Context, req CronCreateRequest) (CronJob, error) {
	s.lastCreate = req
	job := CronJob{
		ID:        "cron-1",
		SessionID: req.SessionID,
		Message:   req.Message,
		CronExpr:  req.CronExpr,
		Timezone:  req.Timezone,
		Enabled:   true,
		CreatedBy: req.CreatedBy,
		CreatedAt: "2026-02-17T10:00:00Z",
		UpdatedAt: "2026-02-17T10:00:00Z",
	}
	s.jobs = append(s.jobs, job)
	return job, nil
}

func (s *fakeCronToolService) ListCronJobs(_ context.Context, req CronListRequest) ([]CronJob, error) {
	if req.SessionID == "" {
		return s.jobs, nil
	}
	filtered := make([]CronJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		if job.SessionID == req.SessionID {
			filtered = append(filtered, job)
		}
	}
	return filtered, nil
}

func (s *fakeCronToolService) DeleteCronJob(_ context.Context, jobID string) (bool, error) {
	for i, job := range s.jobs {
		if job.ID == jobID {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeCronToolService) PauseCronJob(_ context.Context, jobID string) (CronJob, error) {
	for i, job := range s.jobs {
		if job.ID == jobID {
			job.Enabled = false
			s.jobs[i] = job
			return job, nil
		}
	}
	return CronJob{}, nil
}

func (s *fakeCronToolService) ResumeCronJob(_ context.Context, jobID string) (CronJob, error) {
	for i, job := range s.jobs {
		if job.ID == jobID {
			job.Enabled = true
			s.jobs[i] = job
			return job, nil
		}
	}
	return CronJob{}, nil
}

func TestCronToolCreateUsesCurrentSessionID(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"cron"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	fake := &fakeCronToolService{}
	agent.Cron = fake
	runner := NewToolRunner(agent)
	session := agent.NewSession()
	session.ID = "sess-123"

	output, err := runner.Run(context.Background(), session, toolCall("cron", map[string]any{
		"action":    "create",
		"cron_expr": "* * * * *",
		"message":   "scheduled prompt",
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastCreate.SessionID != "sess-123" {
		t.Fatalf("session id = %q, want sess-123", fake.lastCreate.SessionID)
	}
}

func TestCronToolListAndDelete(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"cron"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	fake := &fakeCronToolService{jobs: []CronJob{
		{ID: "cron-1", SessionID: "sess-1", Message: "a"},
		{ID: "cron-2", SessionID: "sess-2", Message: "b"},
	}}
	agent.Cron = fake
	runner := NewToolRunner(agent)
	session := agent.NewSession()
	session.ID = "sess-1"

	listOut, err := runner.Run(context.Background(), session, toolCall("cron", map[string]any{
		"action": "list",
	}))
	if err != nil {
		t.Fatalf("list Run() error: %v", err)
	}
	if listOut.Status != ToolStatusOK {
		t.Fatalf("list status = %q, want ok", listOut.Status)
	}

	deleteOut, err := runner.Run(context.Background(), session, toolCall("cron", map[string]any{
		"action": "delete",
		"job_id": "cron-1",
	}))
	if err != nil {
		t.Fatalf("delete Run() error: %v", err)
	}
	if deleteOut.Status != ToolStatusOK {
		t.Fatalf("delete status = %q, want ok", deleteOut.Status)
	}
}

func toolCall(name string, args map[string]any) ai.ContentBlock {
	return ai.ContentBlock{
		Type:      ai.ContentTypeToolCall,
		Name:      name,
		Arguments: args,
	}
}
