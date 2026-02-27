package agentcore

import (
	"context"
	"testing"
)

type fakeHeartbeatToolService struct {
	state       HeartbeatState
	lastSet     HeartbeatSetRequest
	lastAgentID string
}

func (s *fakeHeartbeatToolService) GetHeartbeat(_ context.Context, agentID string) (HeartbeatState, error) {
	s.lastAgentID = agentID
	return s.state, nil
}

func (s *fakeHeartbeatToolService) SetHeartbeat(_ context.Context, req HeartbeatSetRequest) (HeartbeatState, error) {
	s.lastSet = req
	prompt := ""
	if req.Prompt != nil {
		prompt = *req.Prompt
	}
	ackMaxChars := 0
	if req.AckMaxChars != nil {
		ackMaxChars = *req.AckMaxChars
	}
	timezone := ""
	if req.UserTimezone != nil {
		timezone = *req.UserTimezone
	}
	session := ""
	if req.Session != nil {
		session = *req.Session
	}
	var activeHours *HeartbeatActiveHoursConfig
	if req.ActiveHours != nil {
		value := *req.ActiveHours
		activeHours = &value
	}
	s.state = HeartbeatState{
		Enabled:      true,
		Every:        req.Every,
		Prompt:       prompt,
		AckMaxChars:  ackMaxChars,
		Session:      session,
		ActiveHours:  activeHours,
		UserTimezone: timezone,
	}
	return s.state, nil
}

func (s *fakeHeartbeatToolService) DisableHeartbeat(_ context.Context, agentID string) (HeartbeatState, error) {
	s.lastAgentID = agentID
	s.state = HeartbeatState{}
	return s.state, nil
}

func TestHeartbeatToolSetAndGet(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"heartbeat"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	fake := &fakeHeartbeatToolService{}
	agent.HeartbeatService = fake
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	ackMaxChars := 120
	setOut, err := runner.Run(context.Background(), session, toolCall("heartbeat", map[string]any{
		"action":        "set",
		"every":         "15m",
		"prompt":        "hb-check",
		"ack_max_chars": ackMaxChars,
		"session":       "sess-target",
		"active_hours": map[string]any{
			"start":    "09:00",
			"end":      "18:00",
			"timezone": "America/New_York",
		},
		"user_timezone": "America/New_York",
	}))
	if err != nil {
		t.Fatalf("set Run() error: %v", err)
	}
	if setOut.Status != ToolStatusOK {
		t.Fatalf("set status = %q, want ok", setOut.Status)
	}
	if fake.lastSet.Every != "15m" {
		t.Fatalf("every = %q, want 15m", fake.lastSet.Every)
	}
	if fake.lastSet.Prompt == nil || *fake.lastSet.Prompt != "hb-check" {
		t.Fatalf("prompt = %#v, want hb-check", fake.lastSet.Prompt)
	}
	if fake.lastSet.AckMaxChars == nil || *fake.lastSet.AckMaxChars != ackMaxChars {
		t.Fatalf("ack max chars = %#v, want 120", fake.lastSet.AckMaxChars)
	}
	if fake.lastSet.Session == nil || *fake.lastSet.Session != "sess-target" {
		t.Fatalf("session = %#v, want sess-target", fake.lastSet.Session)
	}
	if fake.lastSet.ActiveHours == nil {
		t.Fatalf("active_hours = nil, want non-nil")
	}
	if fake.lastSet.ActiveHours.Start != "09:00" || fake.lastSet.ActiveHours.End != "18:00" {
		t.Fatalf("active_hours = %#v, want 09:00-18:00", fake.lastSet.ActiveHours)
	}
	if fake.lastSet.ActiveHours.Timezone != "America/New_York" {
		t.Fatalf("active_hours.timezone = %q, want America/New_York", fake.lastSet.ActiveHours.Timezone)
	}
	if fake.lastSet.UserTimezone == nil || *fake.lastSet.UserTimezone != "America/New_York" {
		t.Fatalf("timezone = %#v, want America/New_York", fake.lastSet.UserTimezone)
	}

	getOut, err := runner.Run(context.Background(), session, toolCall("heartbeat", map[string]any{
		"action": "get",
	}))
	if err != nil {
		t.Fatalf("get Run() error: %v", err)
	}
	if getOut.Status != ToolStatusOK {
		t.Fatalf("get status = %q, want ok", getOut.Status)
	}
	result, ok := getOut.Result.(map[string]any)
	if !ok {
		t.Fatalf("get result type = %T, want map[string]any", getOut.Result)
	}
	rawHeartbeat, ok := result["heartbeat"]
	if !ok {
		t.Fatalf("get result missing heartbeat payload")
	}
	state, ok := rawHeartbeat.(HeartbeatState)
	if !ok {
		t.Fatalf("get heartbeat type = %T, want HeartbeatState", rawHeartbeat)
	}
	if state.Session != "sess-target" {
		t.Fatalf("heartbeat session = %q, want sess-target", state.Session)
	}
	if state.ActiveHours == nil || state.ActiveHours.Start != "09:00" || state.ActiveHours.End != "18:00" {
		t.Fatalf("heartbeat active_hours = %#v, want 09:00-18:00", state.ActiveHours)
	}
}

func TestHeartbeatToolDisable(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"heartbeat"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	fake := &fakeHeartbeatToolService{
		state: HeartbeatState{
			Enabled: true,
			Every:   "10m",
		},
	}
	agent.HeartbeatService = fake
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, toolCall("heartbeat", map[string]any{
		"action": "disable",
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastAgentID != agent.ID {
		t.Fatalf("disable agent id = %q, want %q", fake.lastAgentID, agent.ID)
	}
}

func TestHeartbeatToolSetRejectsInvalidAckMaxCharsType(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"heartbeat"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.HeartbeatService = &fakeHeartbeatToolService{}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	_, err = runner.Run(context.Background(), session, toolCall("heartbeat", map[string]any{
		"action":        "set",
		"every":         "15m",
		"ack_max_chars": []any{120},
	}))
	if err == nil {
		t.Fatalf("expected invalid ack_max_chars type error")
	}
}

func TestHeartbeatToolSetRejectsInvalidActiveHoursType(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"heartbeat"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.HeartbeatService = &fakeHeartbeatToolService{}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	_, err = runner.Run(context.Background(), session, toolCall("heartbeat", map[string]any{
		"action":       "set",
		"every":        "15m",
		"active_hours": "09:00-18:00",
	}))
	if err == nil {
		t.Fatalf("expected invalid active_hours type error")
	}
}
