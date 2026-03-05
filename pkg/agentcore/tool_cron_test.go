package agentcore

import (
	"context"
	"strings"
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
		ID:            "cron-1",
		SessionID:     req.SessionID,
		Title:         req.Title,
		Message:       req.Message,
		CronExpr:      req.CronExpr,
		Timezone:      req.Timezone,
		Mode:          req.Mode,
		NotifyActorID: req.NotifyActorID,
		TargetAgent:   req.TargetAgent,
		ModelPolicy:   req.ModelPolicy,
		Enabled:       true,
		CreatedBy:     req.CreatedBy,
		CreatedAt:     "2026-02-17T10:00:00Z",
		UpdatedAt:     "2026-02-17T10:00:00Z",
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
	if fake.lastCreate.NotifyActorID != agent.ID {
		t.Fatalf("notify actor id = %q, want %q", fake.lastCreate.NotifyActorID, agent.ID)
	}
	if fake.lastCreate.Mode != "" {
		t.Fatalf("mode = %q, want empty default input", fake.lastCreate.Mode)
	}
}

func TestCronToolSchemaMentionsModeSelectionExamples(t *testing.T) {
	schema := (&cronTool{}).Schema()
	if !strings.Contains(schema.Description, "scheduled reminders and scheduled tasks") {
		t.Fatalf("schema description = %q", schema.Description)
	}
	properties, ok := schema.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected schema properties")
	}
	mode, ok := properties["mode"].(map[string]any)
	if !ok {
		t.Fatalf("expected mode schema")
	}
	description, _ := mode["description"].(string)
	if !strings.Contains(description, "\"Remind me tomorrow to email Summer\"") {
		t.Fatalf("mode description missing reminder example: %q", description)
	}
	if !strings.Contains(description, "\"Every morning, scan overnight updates and tell me if anything matters\"") {
		t.Fatalf("mode description missing isolated example: %q", description)
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

func TestCronToolCreatePassesScheduledTaskFields(t *testing.T) {
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

	_, err = runner.Run(context.Background(), session, toolCall("cron", map[string]any{
		"action":       "create",
		"title":        "Morning scan",
		"cron_expr":    "0 9 * * *",
		"timezone":     "America/Denver",
		"mode":         "isolated",
		"target_agent": "worker",
		"model_policy": "fast",
		"message":      "Summarize overnight updates.",
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if fake.lastCreate.Title != "Morning scan" {
		t.Fatalf("title = %q", fake.lastCreate.Title)
	}
	if fake.lastCreate.Mode != "isolated" {
		t.Fatalf("mode = %q, want isolated", fake.lastCreate.Mode)
	}
	if fake.lastCreate.TargetAgent != "worker" {
		t.Fatalf("target agent = %q, want worker", fake.lastCreate.TargetAgent)
	}
	if fake.lastCreate.ModelPolicy != "fast" {
		t.Fatalf("model policy = %q, want fast", fake.lastCreate.ModelPolicy)
	}
}

func toolCall(name string, args map[string]any) ai.ContentBlock {
	return ai.ContentBlock{
		Type:      ai.ContentTypeToolCall,
		Name:      name,
		Arguments: args,
	}
}
