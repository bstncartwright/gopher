package agentcore

import (
	"context"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestRunCompactionSummaryModelCallForwardsProviderOptions(t *testing.T) {
	config := defaultConfig()
	config.ProviderOptions = map[string]any{"service_tier": "fast"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "summary"}}

	provider := &mockProvider{
		rounds: []mockRound{{assistant: assistant}},
	}
	agent.Provider = provider

	summary, err := agent.runCompactionSummaryModelCall(context.Background(), agent.NewSession(), "chunk body")
	if err != nil {
		t.Fatalf("runCompactionSummaryModelCall() error: %v", err)
	}
	if summary != "summary" {
		t.Fatalf("summary = %q, want %q", summary, "summary")
	}
	if len(provider.options) == 0 {
		t.Fatalf("expected at least one provider call option")
	}
	if got := provider.options[0].ProviderOptions["service_tier"]; got != "fast" {
		t.Fatalf("provider options service_tier=%#v, want %q", got, "fast")
	}
}
